use anyhow::{anyhow, bail, Context, Result};
use clap::Parser;
use nix::sys::signal::{killpg, Signal};
use nix::unistd::{setpgid, Pid};
use serde::Deserialize;
use signal_hook::consts::signal::{SIGINT, SIGTERM};
use signal_hook::flag;
use std::ffi::OsStr;
use std::fs;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};
use tempfile::TempDir;

#[derive(Parser, Debug)]
#[command(
    name = "compose-rs",
    about = "Rust compositor (GStreamer pipeline + supervised child processes)"
)]
struct Args {
    #[arg(long)]
    input: PathBuf,
    #[arg(long)]
    output: Option<PathBuf>,
    #[arg(long)]
    publisher_log: Option<PathBuf>,
    #[arg(long, default_value_t = 0.0)]
    start: f64,
    #[arg(long)]
    duration: Option<f64>,
    #[arg(long)]
    preview: bool,
    #[arg(long)]
    fps: Option<u32>,
    #[arg(long)]
    width: Option<u32>,
    #[arg(long)]
    height: Option<u32>,
    #[arg(long, default_value_t = 6_000)]
    video_bitrate_kbps: u32,
    #[arg(long, default_value_t = 128)]
    audio_bitrate_kbps: u32,
    #[arg(long, default_value_t = 8192)]
    max_rss_mb: u64,
    #[arg(long, default_value_t = 2)]
    threads: u32,
    #[arg(long)]
    no_hwaccel: bool,
    #[arg(long)]
    verbose: bool,
    #[arg(long, default_value = "gst-launch-1.0")]
    gst_launch: String,
    #[arg(long, default_value = "gst-inspect-1.0")]
    gst_inspect: String,
    #[arg(long, default_value = "/usr/bin/ffprobe")]
    ffprobe: PathBuf,
    #[arg(long, default_value = "/usr/bin/ffmpeg")]
    ffmpeg: PathBuf,
}

#[derive(Deserialize, Debug)]
struct ProbeStream {
    index: u32,
    codec_type: Option<String>,
    codec_name: Option<String>,
    nb_read_packets: Option<String>,
}

#[derive(Deserialize, Debug)]
struct ProbeFormat {
    duration: Option<String>,
}

#[derive(Deserialize, Debug)]
struct ProbeOutput {
    streams: Vec<ProbeStream>,
    format: ProbeFormat,
}

#[derive(Clone, Debug)]
struct Layout {
    cols: u32,
    tile_w: u32,
    tile_h: u32,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum VideoEncoder {
    Vaapi,
    X264,
}

#[derive(Clone, Copy, Debug)]
enum AudioEncoder {
    Fdkaac,
    Voaac,
    AvencAac,
}

#[derive(Clone, Debug)]
struct Settings {
    fps: u32,
    width: u32,
    height: u32,
}

#[derive(Debug, Clone)]
struct StreamLane {
    #[allow(dead_code)]
    ff_index: u32,
    ordinal: u32,
    codec_name: String,
}

#[derive(Debug)]
struct StreamSelection {
    active_video: Vec<StreamLane>,
    active_audio: Vec<StreamLane>,
}

fn main() -> Result<()> {
    let args = Args::parse();
    if !args.input.exists() {
        bail!("input file does not exist: {}", args.input.display());
    }
    if args.threads == 0 {
        bail!("--threads must be >= 1");
    }
    if args.start < 0.0 {
        bail!("--start must be >= 0");
    }
    if let Some(d) = args.duration {
        if d <= 0.0 {
            bail!("--duration must be > 0");
        }
    }

    let stop = Arc::new(AtomicBool::new(false));
    flag::register(SIGTERM, Arc::clone(&stop)).context("register SIGTERM handler")?;
    flag::register(SIGINT, Arc::clone(&stop)).context("register SIGINT handler")?;

    if let Some(path) = args.publisher_log.as_ref() {
        eprintln!(
            "compose-rs: ignoring legacy --publisher-log argument: {}",
            path.display()
        );
    }

    let output = output_path(&args.input, args.output.as_ref());
    let settings = effective_settings(&args);
    let source_probe = ffprobe_json(&args.ffprobe, &args.input)?;

    let needs_trim = args.start > 0.0 || args.duration.is_some();
    let total_duration = if needs_trim {
        Some(parse_duration(&source_probe)?)
    } else {
        None
    };

    let mut maybe_trim_dir: Option<TempDir> = None;
    let input_for_compose = if needs_trim {
        let trim_dir = tempfile::Builder::new()
            .prefix("compose-rs.trim.")
            .tempdir()
            .context("create trim tempdir")?;
        let trimmed = trim_dir.path().join("trimmed.mkv");
        let trim_duration = match args.duration {
            Some(d) => d,
            None => {
                let total = total_duration
                    .ok_or_else(|| anyhow!("missing input duration while resolving trim window"))?;
                (total - args.start).max(0.0)
            }
        };
        if trim_duration <= 0.0 {
            bail!(
                "resolved trim duration is not positive (start={} total={:?})",
                args.start,
                total_duration
            );
        }
        trim_input_with_ffmpeg(
            &args,
            &args.input,
            &trimmed,
            args.start,
            trim_duration,
            &stop,
        )?;
        maybe_trim_dir = Some(trim_dir);
        trimmed
    } else {
        args.input.clone()
    };

    let compose_probe = ffprobe_json(&args.ffprobe, &input_for_compose)?;
    let selection = classify_streams(&compose_probe);
    if selection.active_video.is_empty() {
        bail!(
            "selected input has no video tracks after trimming: {}",
            input_for_compose.display()
        );
    }

    let layout = compute_layout(selection.active_video.len(), settings.width, settings.height);
    let video_bitrate_kbps = if args.preview {
        args.video_bitrate_kbps.min(2500)
    } else {
        args.video_bitrate_kbps
    };

    let video_encoder = choose_video_encoder(&args)?;
    let audio_encoder = choose_audio_encoder(&args)?;
    eprintln!(
        "compose-rs: input={} output={} tracks(video={},audio={}) settings={}x{}@{} video_enc={:?} audio_enc={:?}",
        input_for_compose.display(),
        output.display(),
        selection.active_video.len(),
        selection.active_audio.len(),
        settings.width,
        settings.height,
        settings.fps,
        video_encoder,
        audio_encoder
    );

    compose_with_gstreamer(
        &args,
        &input_for_compose,
        &output,
        &selection.active_video,
        &selection.active_audio,
        &layout,
        &settings,
        video_bitrate_kbps,
        video_encoder,
        audio_encoder,
        &stop,
    )?;

    drop(maybe_trim_dir);
    eprintln!("compose-rs: done -> {}", output.display());
    Ok(())
}

fn effective_settings(args: &Args) -> Settings {
    if args.preview {
        Settings {
            fps: args.fps.unwrap_or(6),
            width: args.width.unwrap_or(640),
            height: args.height.unwrap_or(360),
        }
    } else {
        Settings {
            fps: args.fps.unwrap_or(15),
            width: args.width.unwrap_or(1280),
            height: args.height.unwrap_or(720),
        }
    }
}

fn output_path(input: &Path, override_output: Option<&PathBuf>) -> PathBuf {
    if let Some(path) = override_output {
        return path.clone();
    }
    let stem = input
        .file_stem()
        .map(|s| s.to_string_lossy().to_string())
        .unwrap_or_else(|| "output".to_string());
    let parent = input.parent().unwrap_or_else(|| Path::new("."));
    parent.join(format!("{stem}.composed.mp4"))
}

fn ffprobe_json(ffprobe: &Path, input: &Path) -> Result<ProbeOutput> {
    let output = Command::new(ffprobe)
        .arg("-v")
        .arg("error")
        .arg("-print_format")
        .arg("json")
        .arg("-show_streams")
        .arg("-count_packets")
        .arg("-show_format")
        .arg(input)
        .output()
        .with_context(|| format!("run {}", ffprobe.display()))?;
    if !output.status.success() {
        bail!(
            "ffprobe failed: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        );
    }
    serde_json::from_slice::<ProbeOutput>(&output.stdout).context("parse ffprobe JSON")
}

fn parse_duration(probe: &ProbeOutput) -> Result<f64> {
    let raw = probe
        .format
        .duration
        .as_ref()
        .ok_or_else(|| anyhow!("ffprobe did not return format.duration"))?;
    let duration: f64 = raw.parse().with_context(|| format!("parse duration: {raw}"))?;
    if duration <= 0.0 {
        bail!("invalid input duration: {duration}");
    }
    Ok(duration)
}

fn parse_packet_count(stream: &ProbeStream) -> u64 {
    stream
        .nb_read_packets
        .as_deref()
        .and_then(|v| v.parse::<u64>().ok())
        .unwrap_or(0)
}

fn classify_streams(probe: &ProbeOutput) -> StreamSelection {
    let mut ordered: Vec<&ProbeStream> = probe.streams.iter().collect();
    ordered.sort_by_key(|s| s.index);

    let mut active_video = Vec::new();
    let mut active_audio = Vec::new();

    let mut video_ordinal = 0u32;
    let mut audio_ordinal = 0u32;

    for stream in ordered {
        let packet_count = parse_packet_count(stream);
        match stream.codec_type.as_deref() {
            Some("video") => {
                let lane = StreamLane {
                    ff_index: stream.index,
                    ordinal: video_ordinal,
                    codec_name: stream.codec_name.clone().unwrap_or_default(),
                };
                video_ordinal = video_ordinal.saturating_add(1);
                if packet_count > 0 {
                    active_video.push(lane);
                }
            }
            Some("audio") => {
                let lane = StreamLane {
                    ff_index: stream.index,
                    ordinal: audio_ordinal,
                    codec_name: stream.codec_name.clone().unwrap_or_default(),
                };
                audio_ordinal = audio_ordinal.saturating_add(1);
                if packet_count > 0 {
                    active_audio.push(lane);
                }
            }
            _ => {}
        }
    }

    StreamSelection {
        active_video,
        active_audio,
    }
}

fn compute_layout(video_count: usize, width: u32, height: u32) -> Layout {
    let cols = ((video_count as f64).sqrt().ceil() as u32).max(1);
    let rows = ((video_count as u32 + cols - 1) / cols).max(1);
    let tile_w = (width / cols).max(1);
    let tile_h = (height / rows).max(1);
    Layout {
        cols,
        tile_w,
        tile_h,
    }
}

fn choose_video_encoder(args: &Args) -> Result<VideoEncoder> {
    if args.no_hwaccel {
        return Ok(VideoEncoder::X264);
    }
    let vaapi = has_gst_element(&args.gst_inspect, "vaapih264enc")?;
    if vaapi {
        return Ok(VideoEncoder::Vaapi);
    }
    Ok(VideoEncoder::X264)
}

fn choose_audio_encoder(args: &Args) -> Result<AudioEncoder> {
    if has_gst_element(&args.gst_inspect, "fdkaacenc")? {
        return Ok(AudioEncoder::Fdkaac);
    }
    if has_gst_element(&args.gst_inspect, "voaacenc")? {
        return Ok(AudioEncoder::Voaac);
    }
    if has_gst_element(&args.gst_inspect, "avenc_aac")? {
        return Ok(AudioEncoder::AvencAac);
    }
    bail!("no AAC encoder available (fdkaacenc/voaacenc/avenc_aac not found)")
}

fn has_gst_element(gst_inspect: &str, element: &str) -> Result<bool> {
    let status = Command::new(gst_inspect)
        .arg(element)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .with_context(|| format!("run {} {}", gst_inspect, element))?;
    Ok(status.success())
}

fn trim_input_with_ffmpeg(
    args: &Args,
    input: &Path,
    output: &Path,
    start: f64,
    duration: f64,
    stop: &Arc<AtomicBool>,
) -> Result<()> {
    let mut cmd = Command::new(&args.ffmpeg);
    cmd.arg("-hide_banner")
        .arg("-nostdin")
        .arg("-loglevel")
        .arg(if args.verbose { "info" } else { "error" })
        .arg("-i")
        .arg(input)
        .arg("-ss")
        .arg(format!("{start:.6}"))
        .arg("-t")
        .arg(format!("{duration:.6}"))
        .arg("-map")
        .arg("0")
        .arg("-c")
        .arg("copy")
        .arg("-avoid_negative_ts")
        .arg("make_zero")
        .arg("-y")
        .arg(output);
    run_guarded(
        &mut cmd,
        stop,
        args.max_rss_mb,
        args.verbose,
        "trim input with ffmpeg",
    )
}

#[allow(clippy::too_many_arguments)]
fn compose_with_gstreamer(
    args: &Args,
    input: &Path,
    output: &Path,
    video_streams: &[StreamLane],
    audio_streams: &[StreamLane],
    layout: &Layout,
    settings: &Settings,
    video_bitrate_kbps: u32,
    video_encoder: VideoEncoder,
    audio_encoder: AudioEncoder,
    stop: &Arc<AtomicBool>,
) -> Result<()> {
    let mut cmd = build_gst_compose_command(
        args,
        input,
        output,
        video_streams,
        audio_streams,
        layout,
        settings,
        video_bitrate_kbps,
        video_encoder,
        audio_encoder,
    )?;

    run_guarded(
        &mut cmd,
        stop,
        args.max_rss_mb,
        args.verbose,
        "compose with gstreamer",
    )
}

#[allow(clippy::too_many_arguments)]
fn build_gst_compose_command(
    args: &Args,
    input: &Path,
    output: &Path,
    video_streams: &[StreamLane],
    audio_streams: &[StreamLane],
    layout: &Layout,
    settings: &Settings,
    video_bitrate_kbps: u32,
    video_encoder: VideoEncoder,
    audio_encoder: AudioEncoder,
) -> Result<Command> {
    let mut cmd = Command::new(&args.gst_launch);
    cmd.arg("-e");
    if !args.verbose {
        cmd.arg("-q");
    }

    // Main graph queues are bounded to cap RAM under stalled downstream states.
    let queue_kv = "max-size-time=1000000000 max-size-bytes=4194304 max-size-buffers=64";
    // Branch queues sit before decode (compressed domain). Keep them bounded to cap
    // memory use on sparse-track meetings while preserving timestamp order.
    let video_branch_queue_kv = "max-size-time=5000000000 max-size-bytes=16777216 max-size-buffers=256";
    let audio_branch_queue_kv = "max-size-time=5000000000 max-size-bytes=8388608 max-size-buffers=256";

    cmd.arg("filesrc")
        .arg(format!("location={}", input.to_string_lossy()))
        .arg("!")
        .arg("matroskademux")
        .arg("name=demux");

    cmd.arg("compositor")
        .arg("name=comp")
        .arg("background=black")
        .arg("ignore-inactive-pads=true");

    for i in 0..video_streams.len() {
        let xpos = (i as u32 % layout.cols) * layout.tile_w;
        let ypos = (i as u32 / layout.cols) * layout.tile_h;
        cmd.arg(format!("sink_{}::xpos={}", i, xpos))
            .arg(format!("sink_{}::ypos={}", i, ypos))
            .arg(format!("sink_{}::width={}", i, layout.tile_w))
            .arg(format!("sink_{}::height={}", i, layout.tile_h));
    }

    cmd.arg("!")
        .arg("queue")
        .args(queue_kv.split_whitespace())
        .arg("!")
        .arg("videoconvert")
        .arg("!")
        .arg("videoscale")
        .arg("!")
        .arg("videorate")
        .arg("!");

    match video_encoder {
        VideoEncoder::Vaapi => {
            cmd.arg(format!(
                "video/x-raw,format=NV12,width={},height={},framerate={}/1",
                settings.width, settings.height, settings.fps
            ))
            .arg("!")
            .arg("vaapih264enc")
            .arg(format!("bitrate={}", video_bitrate_kbps))
            .arg("keyframe-period=60")
            .arg("rate-control=cbr");
        }
        VideoEncoder::X264 => {
            cmd.arg(format!(
                "video/x-raw,format=I420,width={},height={},framerate={}/1",
                settings.width, settings.height, settings.fps
            ))
            .arg("!")
            .arg("x264enc")
            .arg("speed-preset=veryfast")
            .arg("tune=zerolatency")
            .arg(format!("threads={}", args.threads))
            .arg(format!("bitrate={}", video_bitrate_kbps));
        }
    }

    cmd.arg("!")
        .arg("h264parse")
        .arg("!")
        .arg("queue")
        .args(queue_kv.split_whitespace())
        .arg("!")
        .arg("mux.");

    if !audio_streams.is_empty() {
        cmd.arg("audiomixer")
            .arg("name=amix")
            .arg("ignore-inactive-pads=true")
            .arg("!")
            .arg("queue")
            .args(queue_kv.split_whitespace())
            .arg("!")
            .arg("audioconvert")
            .arg("!")
            .arg("audioresample")
            .arg("!")
            .arg("audio/x-raw,rate=48000,channels=2")
            .arg("!");

        match audio_encoder {
            AudioEncoder::Fdkaac => {
                cmd.arg("fdkaacenc")
                    .arg(format!("bitrate={}", args.audio_bitrate_kbps * 1000));
            }
            AudioEncoder::Voaac => {
                cmd.arg("voaacenc")
                    .arg(format!("bitrate={}", args.audio_bitrate_kbps * 1000));
            }
            AudioEncoder::AvencAac => {
                cmd.arg("avenc_aac")
                    .arg(format!("bit_rate={}", args.audio_bitrate_kbps * 1000));
            }
        }

        cmd.arg("!")
            .arg("queue")
            .args(queue_kv.split_whitespace())
            .arg("!")
            .arg("mux.");
    }

    cmd.arg("mp4mux")
        .arg("name=mux")
        .arg("faststart=true")
        .arg("!")
        .arg("filesink")
        .arg(format!("location={}", output.to_string_lossy()))
        .arg("sync=false")
        .arg("async=false");

    for (branch_idx, lane) in video_streams.iter().enumerate() {
        let use_decoder = if lane.codec_name.eq_ignore_ascii_case("vp8") {
            "vp8dec"
        } else if lane.codec_name.eq_ignore_ascii_case("vp9") {
            "vp9dec"
        } else if lane.codec_name.eq_ignore_ascii_case("h264") {
            "avdec_h264"
        } else {
            "decodebin"
        };

        cmd.arg(format!("demux.video_{}", lane.ordinal))
            .arg("!")
            .arg("queue")
            .args(video_branch_queue_kv.split_whitespace())
            .arg("!")
            .arg(use_decoder)
            .arg("!")
            .arg("videoconvert")
            .arg("!")
            .arg("videoscale")
            .arg("!")
            .arg(format!(
                "video/x-raw,width={},height={}",
                layout.tile_w, layout.tile_h
            ))
            .arg("!")
            .arg(format!("comp.sink_{}", branch_idx));
    }

    for (branch_idx, lane) in audio_streams.iter().enumerate() {
        let use_decoder = if lane.codec_name.eq_ignore_ascii_case("opus") {
            "opusdec"
        } else if lane.codec_name.eq_ignore_ascii_case("aac") {
            "avdec_aac"
        } else {
            "decodebin"
        };

        cmd.arg(format!("demux.audio_{}", lane.ordinal))
            .arg("!")
            .arg("queue")
            .args(audio_branch_queue_kv.split_whitespace())
            .arg("!")
            .arg(use_decoder)
            .arg("!")
            .arg("audioconvert")
            .arg("!")
            .arg("audioresample")
            .arg("!")
            .arg("audio/x-raw,rate=48000,channels=2")
            .arg("!")
            .arg(format!("amix.sink_{}", branch_idx));
    }

    Ok(cmd)
}

fn run_guarded(
    cmd: &mut Command,
    stop: &Arc<AtomicBool>,
    max_rss_mb: u64,
    verbose: bool,
    context: &str,
) -> Result<()> {
    if verbose {
        eprintln!("compose-rs: running {}", fmt_command(cmd));
    }
    set_process_group(cmd)?;
    let mut child = cmd
        .spawn()
        .with_context(|| format!("spawn process for {context}"))?;
    let child_pid = Pid::from_raw(child.id() as i32);
    let rss_limit_kb = max_rss_mb.saturating_mul(1024);
    let mut sent_sigterm = false;
    let mut sigterm_deadline = Instant::now();

    loop {
        if let Some(status) = child.try_wait().context("poll child status")? {
            if status.success() {
                return Ok(());
            }
            return Err(anyhow!("{context} failed with status {status}"));
        }

        if stop.load(Ordering::Relaxed) {
            if !sent_sigterm {
                let _ = killpg(child_pid, Signal::SIGTERM);
                sent_sigterm = true;
                sigterm_deadline = Instant::now() + Duration::from_secs(2);
            } else if Instant::now() >= sigterm_deadline {
                let _ = killpg(child_pid, Signal::SIGKILL);
                return Err(anyhow!("{context} stopped by signal"));
            }
        }

        if rss_limit_kb > 0 {
            if let Some(rss_kb) = read_rss_kb(child.id()) {
                if rss_kb > rss_limit_kb {
                    let _ = killpg(child_pid, Signal::SIGTERM);
                    thread::sleep(Duration::from_millis(400));
                    let _ = killpg(child_pid, Signal::SIGKILL);
                    return Err(anyhow!(
                        "{context} exceeded memory guard: {} MB > {} MB",
                        rss_kb / 1024,
                        max_rss_mb
                    ));
                }
            }
        }

        thread::sleep(Duration::from_millis(200));
    }
}

fn set_process_group(cmd: &mut Command) -> Result<()> {
    unsafe {
        cmd.pre_exec(|| {
            setpgid(Pid::from_raw(0), Pid::from_raw(0))
                .map_err(|err| std::io::Error::other(err.to_string()))
        });
    }
    Ok(())
}

fn read_rss_kb(pid: u32) -> Option<u64> {
    let content = fs::read_to_string(format!("/proc/{pid}/status")).ok()?;
    for line in content.lines() {
        if !line.starts_with("VmRSS:") {
            continue;
        }
        let mut parts = line.split_whitespace();
        let _ = parts.next();
        let value = parts.next()?.parse::<u64>().ok()?;
        return Some(value);
    }
    None
}

fn fmt_command(cmd: &Command) -> String {
    let mut out = shell_escape(cmd.get_program().to_string_lossy().as_ref());
    for arg in cmd.get_args() {
        out.push(' ');
        out.push_str(&shell_escape(arg_to_str(arg)));
    }
    out
}

fn arg_to_str(arg: &OsStr) -> &str {
    arg.to_str().unwrap_or("<non-utf8>")
}

fn shell_escape(value: &str) -> String {
    if value.is_empty() {
        return "''".to_string();
    }
    if value
        .bytes()
        .all(|b| b.is_ascii_alphanumeric() || b"-_./:=+,%".contains(&b))
    {
        return value.to_string();
    }
    format!("'{}'", value.replace('\'', "'\\''"))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn probe_stream(index: u32, codec_type: &str, codec_name: &str, packets: u64) -> ProbeStream {
        ProbeStream {
            index,
            codec_type: Some(codec_type.to_string()),
            codec_name: Some(codec_name.to_string()),
            nb_read_packets: Some(packets.to_string()),
        }
    }

    fn test_args() -> Args {
        Args {
            input: PathBuf::from("/tmp/in.mkv"),
            output: None,
            publisher_log: None,
            start: 0.0,
            duration: None,
            preview: true,
            fps: Some(6),
            width: Some(640),
            height: Some(360),
            video_bitrate_kbps: 2500,
            audio_bitrate_kbps: 128,
            max_rss_mb: 2048,
            threads: 2,
            no_hwaccel: false,
            verbose: false,
            gst_launch: "gst-launch-1.0".to_string(),
            gst_inspect: "gst-inspect-1.0".to_string(),
            ffprobe: PathBuf::from("/usr/bin/ffprobe"),
            ffmpeg: PathBuf::from("/usr/bin/ffmpeg"),
        }
    }

    #[test]
    fn classify_streams_uses_type_ordinals_and_drain() {
        let probe = ProbeOutput {
            streams: vec![
                probe_stream(0, "video", "vp8", 10),
                probe_stream(1, "audio", "opus", 12),
                probe_stream(2, "video", "vp8", 0),
                probe_stream(3, "audio", "opus", 0),
                probe_stream(4, "video", "vp8", 8),
            ],
            format: ProbeFormat {
                duration: Some("10.0".to_string()),
            },
        };

        let selection = classify_streams(&probe);
        assert_eq!(selection.active_video.len(), 2);
        assert_eq!(selection.active_audio.len(), 1);

        assert_eq!(selection.active_video[0].ordinal, 0);
        assert_eq!(selection.active_video[0].ff_index, 0);
        assert_eq!(selection.active_video[1].ordinal, 2);
        assert_eq!(selection.active_video[1].ff_index, 4);

        assert_eq!(selection.active_audio[0].ordinal, 0);
        assert_eq!(selection.active_audio[0].ff_index, 1);
    }

    #[test]
    fn compute_layout_scales_to_participants() {
        let layout = compute_layout(5, 1280, 720);
        assert_eq!(layout.cols, 3);
        assert_eq!(layout.tile_w, 426);
        assert_eq!(layout.tile_h, 360);
    }

    #[test]
    fn build_pipeline_uses_matroska_demux_ordinals() -> Result<()> {
        let args = test_args();
        let settings = effective_settings(&args);
        let layout = compute_layout(2, settings.width, settings.height);

        let videos = vec![
            StreamLane {
                ff_index: 0,
                ordinal: 0,
                codec_name: "vp8".to_string(),
            },
            StreamLane {
                ff_index: 2,
                ordinal: 1,
                codec_name: "vp8".to_string(),
            },
        ];
        let audios = vec![StreamLane {
            ff_index: 1,
            ordinal: 0,
            codec_name: "opus".to_string(),
        }];

        let cmd = build_gst_compose_command(
            &args,
            Path::new("/tmp/in.mkv"),
            Path::new("/tmp/out.mp4"),
            &videos,
            &audios,
            &layout,
            &settings,
            2500,
            VideoEncoder::X264,
            AudioEncoder::AvencAac,
        )?;

        let rendered = fmt_command(&cmd);
        assert!(rendered.contains("matroskademux"));
        assert!(rendered.contains("demux.video_0"));
        assert!(rendered.contains("demux.video_1"));
        assert!(rendered.contains("demux.audio_0"));
        assert!(!rendered.contains("uridecodebin"));
        assert!(!rendered.contains("ts-offset="));
        assert!(rendered.contains("compositor name=comp background=black ignore-inactive-pads=true"));
        assert!(rendered.contains("audiomixer name=amix ignore-inactive-pads=true"));
        Ok(())
    }
}
