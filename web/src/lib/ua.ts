// A short "Browser · System" name for a session from its User-Agent header: "Chrome · macOS".
// Order matters: Edge and Opera carry "Chrome" in their string, Chrome carries "Safari", iPhones carry "Mac OS X".
const browsers: [RegExp, string][] = [
  [/Edg(e|A|iOS)?\//, "Edge"],
  [/OPR\/|Opera/, "Opera"],
  [/Firefox\/|FxiOS\//, "Firefox"],
  [/Chrome\/|CriOS\//, "Chrome"],
  [/Safari\//, "Safari"],
];
const systems: [RegExp, string][] = [
  [/iPhone|iPad|iPod/, "iOS"],
  [/Android/, "Android"],
  [/Windows/, "Windows"],
  [/Mac OS X|Macintosh/, "macOS"],
  [/CrOS/, "ChromeOS"],
  [/Linux|X11/, "Linux"],
];

export function describeUserAgent(ua: string): { browser: string; system: string } {
  const browser = browsers.find(([re]) => re.test(ua))?.[1] ?? "";
  const system = systems.find(([re]) => re.test(ua))?.[1] ?? "";
  return { browser, system };
}
