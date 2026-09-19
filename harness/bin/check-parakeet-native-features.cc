// Research-only feature probe for the patched sherpa-onnx v1.13.7 C++ core.
// Build against the isolated patched source/core archive, NOT installed libs.
// Usage: probe --self-test
//        probe input.f32 output.f32
// Raw files are native little-endian float32 mono 16kHz PCM/features.
// Output features are row-major [floor(samples/160),128], except <2 frames
// produce an empty output. No model or GPU is needed. Keep audio files private.
#include <cmath>
#include <fstream>
#include <iostream>
#include <stdexcept>
#include <string>
#include <vector>

#include "sherpa-onnx/csrc/offline-stream.h"

std::vector<float> Extract(const std::vector<float>& samples) {
  sherpa_onnx::FeatureExtractorConfig config;
  config.feature_dim = 128;
  config.sampling_rate = 16000;
  config.nemo_normalize_type = "per_feature";
  config.parakeet_reference_frontend = true;
  sherpa_onnx::OfflineStream stream(config);
  if (!samples.empty()) {
    stream.AcceptWaveform(16000, samples.data(), samples.size());
  }
  const auto first = stream.GetFrames();
  const auto second = stream.GetFrames();
  if (first != second) throw std::runtime_error("GetFrames is not deterministic");
  for (float value : first) {
    if (!std::isfinite(value)) throw std::runtime_error("non-finite feature");
  }
  return first;
}

int main(int argc, char** argv) {
  try {
    if (argc == 2 && std::string(argv[1]) == "--self-test") {
      for (int n : {0, 1, 159, 160, 319, 320, 321, 399, 400, 479, 480, 511, 512,
                    15999, 16000, 16001}) {
        for (bool silence : {false, true}) {
          std::vector<float> samples(n);
          if (!silence) {
            for (int i = 0; i < n; ++i) samples[i] = .1f * std::sin(i * .17f);
          }
          const auto features = Extract(samples);
          const size_t expected = n / 160 < 2 ? 0 : (n / 160) * 128;
          if (features.size() != expected) throw std::runtime_error("frame count mismatch");
        }
      }
      std::cout << "32 boundary/silence cases: finite, exact frame count, deterministic\n";
      return 0;
    }
    if (argc != 3) throw std::runtime_error("usage: probe --self-test OR input.f32 output.f32");
    std::ifstream input(argv[1], std::ios::binary | std::ios::ate);
    if (!input) throw std::runtime_error("cannot open input");
    const auto size = input.tellg();
    if (size < 0 || size % sizeof(float)) throw std::runtime_error("invalid float32 file");
    std::vector<float> samples(static_cast<size_t>(size) / sizeof(float));
    input.seekg(0);
    input.read(reinterpret_cast<char*>(samples.data()), size);
    if (!input && size != 0) throw std::runtime_error("incomplete input read");
    const auto features = Extract(samples);
    std::ifstream existing(argv[2]);
    if (existing.good()) throw std::runtime_error("output exists; choose a new path");
    std::ofstream output(argv[2], std::ios::binary);
    output.write(reinterpret_cast<const char*>(features.data()), features.size() * sizeof(float));
    if (!output) throw std::runtime_error("output write failed");
    std::cout << samples.size() << " samples -> " << features.size() / 128 << " x 128\n";
    return 0;
  } catch (const std::exception& e) {
    std::cerr << e.what() << '\n';
    return 1;
  }
}
