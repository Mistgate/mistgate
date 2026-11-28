import { useLayoutEffect, useRef, type RefObject } from "react";
import * as ace from "ace-builds";
import "ace-builds/src-noconflict/mode-yaml";
import "ace-builds/src-noconflict/theme-github_dark";
import "ace-builds/src-noconflict/theme-github_light_default";
import "ace-builds/src-noconflict/ext-searchbox";
import { useTheme } from "@/lib/theme";
import { activeBlocks, type Source } from "./mihomo-source";

const Range = ace.Range;
const directive = /^# @mistgate-(?:if[ \t]|end[ \t]*$)/;

function installYamlMode(session: ace.Ace.EditSession) {
  // Ace's YAML folder stops at column-zero comments. For fold calculations only, treat
  // Mistgate directives like blank lines so sections can contain conditional list items.
  const { Mode } = ace.require("ace/mode/yaml");
  const mode = new Mode();
  const stock = mode.foldingRules;
  const foldingSession: ace.Ace.EditSession = Object.create(session);
  foldingSession.getLine = (row) => {
    const line = session.getLine(row);
    return directive.test(line) ? "" : line;
  };
  mode.foldingRules = {
    getFoldWidgetRange(_session: ace.Ace.EditSession, style: string, row: number) {
      return stock.getFoldWidgetRange(foldingSession, style, row);
    },
    getFoldWidget(_session: ace.Ace.EditSession, style: string, row: number) {
      const line = foldingSession.getLine(row);
      const indent = line.search(/\S/);
      if (indent < 0 || line[indent] === "#") return "";
      let next = row + 1;
      while (next < session.getLength()) {
        const child = foldingSession.getLine(next++);
        const childIndent = child.search(/\S/);
        if (childIndent < 0) continue;
        if (childIndent > indent || (childIndent === indent && child[childIndent] === "-" && line[indent] !== "-")) {
          return stock.getFoldWidgetRange(foldingSession, style, row) ? "start" : "";
        }
        break;
      }
      return "";
    },
  };
  session.setMode(mode);
}

type Props = {
  editorRef: RefObject<ace.Ace.Editor | null>;
  text: string;
  label: string;
  description?: string;
  readOnly: boolean;
  blocks?: Source["blocks"];
  variant?: string;
  onChange?: (text: string) => void;
};

export function MihomoSourceEditor({ editorRef, text, label, description, readOnly, blocks, variant = "all", onChange }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const latestChange = useRef(onChange);
  const applying = useRef(false);
  const dark = useTheme() === "dark";

  useLayoutEffect(() => {
    latestChange.current = onChange;
  }, [onChange]);

  useLayoutEffect(() => {
    const instance = ace.edit(host.current!, {
      useWorker: false,
      showPrintMargin: false,
      showLineNumbers: true,
      showFoldWidgets: true,
      tabSize: 2,
      useSoftTabs: true,
      enableKeyboardAccessibility: true,
      fontFamily: "var(--font-mono)",
      fontSize: 12,
      useResizeObserver: true,
    });
    instance.session.setNewLineMode("unix");
    installYamlMode(instance.session);
    // Do not request Ace extensions that are not bundled with the application.
    for (const name of ["gotoline", "showSettingsMenu", "openCommandPallete", "openCommandPalette", "modeSelect"]) instance.commands.removeCommand(name);
    const change = () => {
      if (!applying.current) latestChange.current?.(instance.getValue());
    };
    instance.session.on("change", change);
    editorRef.current = instance;
    return () => {
      instance.session.off("change", change);
      instance.destroy();
      editorRef.current = null;
    };
  }, [editorRef]);

  useLayoutEffect(() => {
    const instance = editorRef.current!;
    if (text === instance.getValue()) return;
    const cursor = instance.getCursorPosition();
    const top = instance.session.getScrollTop();
    applying.current = true;
    instance.session.setValue(text);
    applying.current = false;
    instance.moveCursorToPosition(cursor);
    instance.clearSelection();
    instance.session.setScrollTop(top);
  }, [editorRef, text]);

  useLayoutEffect(() => {
    editorRef.current!.setTheme(dark ? "ace/theme/github_dark" : "ace/theme/github_light_default");
  }, [dark, editorRef]);

  useLayoutEffect(() => {
    const instance = editorRef.current!;
    instance.setReadOnly(readOnly);
    instance.setOption("textInputAriaLabel", label);
    const input = host.current!.querySelector("textarea")!;
    if (description) input.setAttribute("aria-describedby", description);
    else input.removeAttribute("aria-describedby");
  }, [description, editorRef, label, readOnly]);

  useLayoutEffect(() => {
    if (!blocks || variant === "all") return;
    const session = editorRef.current!.session;
    const markers: number[] = [];
    const chosen = activeBlocks(blocks, variant);
    for (const [i, { open, close, parent }] of blocks.entries()) {
      if (!chosen[i] && (parent < 0 || chosen[parent])) {
        markers.push(session.addMarker(new Range(open, 0, close, 1), "mt-dropped", "fullLine", true));
      }
    }
    return () => { for (const marker of markers) session.removeMarker(marker); };
  }, [blocks, editorRef, variant]);
  return <div ref={host} className="mt-source" data-noi18n />;
}
