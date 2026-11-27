import { useMemo, type ReactNode } from "react";
import Prism from "prismjs/components/prism-core.js";
import "prismjs/components/prism-yaml.js";

function render(tokens: (string | Prism.Token)[]): ReactNode[] {
  return tokens.map((token, index) => {
    if (typeof token === "string") return token;
    const value = typeof token.content === "string" ? token.content : render(Array.isArray(token.content) ? token.content : [token.content]);
    const kind = token.type === "scalar" ? "string" : token.type;
    return <span key={index} className={`mt-token-${kind}`}>{value}</span>;
  });
}

export function MihomoYamlPreview({ text }: { text: string }) {
  const highlighted = useMemo(() => render(Prism.tokenize(text, Prism.languages.yaml!)), [text]);
  return <code>{highlighted}</code>;
}
