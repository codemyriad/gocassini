import type { Binds } from "./bind";
import { pad } from "./util";

export const NUMS_STILL = 30;

export const greetings = [
  ["Hello", "English"], ["Bonjour", "French"], ["Hallo", "German"], ["Ciao", "Italian"], ["Hola", "Spanish"],
  ["Olá", "Portuguese"], ["Goedendag", "Dutch"], ["Cześć", "Polish"], ["Hej", "Danish"], ["Hallå", "Swedish"],
  ["Hei", "Finnish"], ["Tere", "Estonian"], ["Sveiki", "Latvian"], ["Labas", "Lithuanian"], ["Szia", "Hungarian"],
  ["Ahoj", "Czech"], ["Dobrý deň", "Slovak"], ["Živjo", "Slovenian"], ["Bok", "Croatian"], ["Bună", "Romanian"],
  ["Здравейте", "Bulgarian"], ["Γεια σας", "Greek"], ["Bonġu", "Maltese"], ["Привет", "Russian"], ["Привіт", "Ukrainian"],
] as const;

export const ONE_BARS = 12;

/** The numbers row at whole tick `t`. */
export function nums(t: number): Binds {
  const b: Binds = {};
  const n = greetings.length;
  const cur = Math.floor(t / 14) % n;
  greetings.forEach((_, i) => {
    const rel = ((i - cur) % n + n) % n;
    b["g" + i] = { s: { opacity: rel === 0 ? 1 : 0, transform: "translateZ(0) translateY(" + (rel === 0 ? 0 : rel === n - 1 ? -16 : 16) + "px)" } };
  });

  const sec = 14 + Math.floor(t / 10);
  b.ztime = { t: pad(Math.floor(sec / 60)) + ":" + pad(sec % 60) };
  b.dot = { s: { opacity: Math.floor(t / 5) % 2 === 0 ? 1 : 0.3 } };

  const litP = Math.floor(t / 9) % 5;
  for (let i = 0; i < 5; i++) {
    b["p" + i] = {
      s: {
        color: i === litP ? "var(--color-primary)" : "color-mix(in oklab, var(--color-base-content) 40%, transparent)",
        transform: i === litP ? "translateZ(0) translateY(-2px)" : "translateZ(0)",
      },
    };
  }

  const p = t % 45;
  const recording = p < 18;
  for (let i = 0; i < ONE_BARS; i++) {
    const v = Math.abs(Math.sin((i + t) * 0.7) * Math.sin(i * 0.45 + t * 0.23));
    b["ob" + i] = { s: { height: (recording ? Math.round(4 + v * 18) : 3) + "px" } };
  }
  const waveOn = p < 19 || p >= 43;
  const fileOn = p >= 19 && p < 41;
  b.owave = { s: { opacity: waveOn ? 1 : 0, transform: waveOn ? "translateZ(0)" : "translateZ(0) translateX(24px)" } };
  b.ofile = { s: { opacity: fileOn ? 1 : 0, transform: fileOn ? "translateZ(0)" : "translateZ(0) translateX(" + (waveOn ? -12 : 12) + "px)" } };
  return b;
}
