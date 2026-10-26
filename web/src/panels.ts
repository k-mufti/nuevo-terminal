import type { PanelDef } from "./types";

// Order = number key. Categories match server/feeds.json.
export const PANELS: PanelDef[] = [
  { code: "WRLD", aliases: ["WORLD", "W"], cat: "world", title: "World" },
  { code: "US", aliases: ["USA", "NATION"], cat: "us", title: "United States" },
  { code: "MKT", aliases: ["MARKETS", "MARKET", "BIZ", "FIN"], cat: "markets", title: "Markets & Business" },
  { code: "TECH", aliases: ["T", "TECHNOLOGY"], cat: "tech", title: "Technology" },
  { code: "MIDE", aliases: ["ME", "MIDEAST", "MIDDLEEAST"], cat: "mideast", title: "Middle East" },
  { code: "SCI", aliases: ["SCIENCE"], cat: "science", title: "Science" },
  { code: "SPRT", aliases: ["SPORT", "SPORTS"], cat: "sports", title: "Sports" },
];

export function findPanel(cmd: string): number {
  const c = cmd.toUpperCase().replace(/\s+/g, "");
  return PANELS.findIndex((p) => p.code === c || p.aliases.includes(c));
}
