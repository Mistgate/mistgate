import { Field } from "@base-ui/react/field";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { AccentPicker } from "@/components/accent-picker";
import { CopyButton } from "@/components/copy-button";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { Avatar, Card, Check, Chip, Kbd, PageTitle, SectionLabel } from "@/components/ui/bits";
import { Button, type ButtonVariant } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Flag } from "@/components/ui/flag";
import { IconButton } from "@/components/ui/icon-button";
import { Icon, NavIcon, navPaths, type NavIconName, type Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Banner, Callout, EmptyState, Notice } from "@/components/ui/notice";
import { PopFooter, PopRow, Popover } from "@/components/ui/popover";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { Pending, QueryError, Skeleton } from "@/components/ui/query-error";
import { Row, RowHead } from "@/components/ui/row";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { StatusDot, StatusGlyph, StatusPill, type StatusKind } from "@/components/ui/status";
import { Stepper } from "@/components/ui/stepper";
import { Switch } from "@/components/ui/switch";
import { Tabs } from "@/components/ui/tabs";
import { fieldClass, TextArea, TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { Tip } from "@/components/ui/tooltip";
import { cx } from "@/lib/cx";
import { useTheme } from "@/lib/theme";
import { Shot } from "./dev-kit-redesign";

// Development only (/dev-kit?only=foundation): the base components of the redesign in every variant and state, in the theme
// and the language the page is in (the toggles at the top; Persian gives right to left). One block per kind, each with
// data-shot for the screenshot script. A state the pointer or the keyboard makes (focus, an open list) is drawn by hand
// here so a still picture shows it; a window or a popover opens from /dev-kit?only=foundation&open=modal|wide|sheet|popover.

const buttonVariants: ButtonVariant[] = ["primary", "secondary", "outline", "ghost", "danger", "ghostDanger"];
const sizes = ["lg", "md", "sm", "xs"] as const;
const sizePx = { lg: 40, md: 32, sm: 28, xs: 24 };
const statusKinds: StatusKind[] = ["ok", "warn", "bad", "off", "busy", "blip"];
const tones: Tone[] = ["lavender", "sky", "sand", "sage", "mint", "rose"];
const protocols = ["hysteria2", "vless", "awg", "awg-old", "trusttunnel", "relay", "dns"] as const;
const protocolLabel: Record<string, string> = { relay: "Relay", dns: "DNS tunnel", "awg-old": "AmneziaWG (old)" };
const focusRing = "outline-2 outline-offset-2 outline-accent-mark";

/** A token colour with its name and the colour the browser resolved it to (it follows the theme). */
function Swatch({ token, label }: { token: string; label?: string }) {
  useTheme(); // a theme change re-renders this, and the effect below reads the colour again
  const ref = useRef<HTMLSpanElement>(null);
  const [value, setValue] = useState("");
  useEffect(() => {
    if (ref.current) setValue(getComputedStyle(ref.current).backgroundColor);
  });
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      <span ref={ref} className="size-8 flex-none rounded-ctl border border-line" style={{ background: `var(--${token})` }} />
      <span className="flex min-w-0 flex-col text-xs leading-4">
        <span className="truncate text-fg">{label ?? token}</span>
        <span className="truncate font-mono text-[11px] text-muted" dir="ltr">
          {value}
        </span>
      </span>
    </div>
  );
}

function Group({ title, children, className }: { title: string; children: ReactNode; className?: string }) {
  return (
    <div className="flex flex-col gap-2">
      <div className="text-xs text-muted">{title}</div>
      <div className={className ?? "flex flex-wrap items-center gap-2"}>{children}</div>
    </div>
  );
}

function Tokens() {
  return (
    <Card className="flex flex-col gap-5 p-5">
      <Group title="surfaces and text (bg, surface, raise = surface-2, hair = border, step; text, sec, muted)" className="grid grid-cols-2 gap-3 md:grid-cols-4">
        {["bg", "surface", "surface-2", "border", "step", "text", "sec", "muted"].map((t) => (
          <Swatch key={t} token={t} />
        ))}
      </Group>
      <Group title="the accent in its jobs (fill, mark, text, soft, line)" className="grid grid-cols-2 gap-3 md:grid-cols-5">
        {["accent", "accent-mark", "accent-text", "accent-soft", "accent-line"].map((t) => (
          <Swatch key={t} token={t} />
        ))}
      </Group>
      <Group title="tones: colour and background" className="grid grid-cols-2 gap-3 md:grid-cols-4">
        {[...tones.flatMap((t) => [`tone-${t}`, `tone-${t}-bg`]), "tone-blip"].map((t) => (
          <Swatch key={t} token={t} />
        ))}
      </Group>
      <Group title="status: warn, danger, ok (always mint)" className="grid grid-cols-2 gap-3 md:grid-cols-4">
        {["warning", "warning-soft", "danger", "danger-soft", "ok"].map((t) => (
          <Swatch key={t} token={t} />
        ))}
      </Group>
    </Card>
  );
}

function Buttons() {
  return (
    <Card className="flex flex-col gap-5 p-5">
      {sizes.map((size) => (
        <Group key={size} title={`${size} = ${sizePx[size]}px: normal, with an icon, disabled, focus (drawn)`}>
          {buttonVariants.map((v) => (
            <Button key={v} variant={v} size={size}>
              {v}
            </Button>
          ))}
          <Button size={size}>
            <Icon name="plus" size={16} />
            With icon
          </Button>
          <Button variant="primary" size={size}>
            <Icon name="plus" size={16} />
            Primary
          </Button>
          <Button size={size} disabled>
            Disabled
          </Button>
          <Button variant="primary" size={size} disabled>
            Disabled
          </Button>
          <Button size={size} className={focusRing}>
            Focus
          </Button>
          <Button variant="primary" size={size} className={focusRing}>
            Focus
          </Button>
        </Group>
      ))}
      <Group title="copy button (click it: the icon and the word swap)">
        <CopyButton value="demo" />
        <CopyButton value="demo" size="md" label="Copy the link" />
        <CopyButton value="demo" size="lg" variant="primary" label="Copy the command" />
      </Group>
      <Group title="icon buttons: square 40, close 32, flat 28, field 40, round 26, round accent 26">
        <IconButton aria-label="search">
          <Icon name="search" size={16} />
        </IconButton>
        <IconButton aria-label="search" disabled>
          <Icon name="search" size={16} />
        </IconButton>
        <IconButton variant="close" aria-label="close">
          <Icon name="x" size={16} />
        </IconButton>
        <IconButton variant="flat" aria-label="more">
          <Icon name="ellipsis" size={16} />
        </IconButton>
        <IconButton variant="field" aria-label="copy">
          <Icon name="copy" size={16} />
        </IconButton>
        <IconButton variant="round" aria-label="minus">
          <Icon name="minus" size={12} />
        </IconButton>
        <IconButton variant="roundAccent" aria-label="plus">
          <Icon name="plus" size={12} />
        </IconButton>
        <IconButton aria-label="focus" className={focusRing}>
          <Icon name="gear" size={16} />
        </IconButton>
      </Group>
    </Card>
  );
}

function Fields() {
  const [sel, setSel] = useState("a");
  const options = [
    { value: "a", label: "Frankfurt" },
    { value: "b", label: "Amsterdam", hint: "slow today" },
    { value: "c", label: "Helsinki", disabled: true },
  ];
  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Card className="flex flex-col gap-4 p-5">
        <TextField label="Name" placeholder="alice" hint="Shown in the list" />
        <TextField label="Name, typed" defaultValue="bob" />
        <Field.Root className="flex flex-col gap-1.5">
          <span className="text-[13px] text-sec">Focus (drawn by hand)</span>
          <input className={cx(fieldClass, "h-10 px-3 border-accent-line shadow-[0_0_0_3px_var(--focus-ring)]")} defaultValue="alice" aria-label="focus" />
        </Field.Root>
        <TextField label="Error" defaultValue="203.0.113.999" error="This address is not valid" mono />
        <TextField label="Disabled" defaultValue="locked" disabled />
        <TextField label="Mono" defaultValue="203.0.113.7:443" mono />
        <TextField aria-label="Search" placeholder="Search" search />
        <TextField label="With a tone" icon="key" tone="lavender" placeholder="secret" />
      </Card>
      <Card className="flex flex-col gap-4 p-5">
        <TextArea label="Notes" placeholder="Anything" hint="Grows by the handle" rows={3} />
        <TextArea label="Error" defaultValue="oops" error="Too short" rows={2} />
        <TextArea label="Disabled" defaultValue="locked" disabled rows={2} />
        <div className="flex flex-col gap-1.5">
          <span className="text-[13px] text-sec">Select</span>
          <Select aria-label="node" value={sel} onValueChange={setSel} options={options} />
        </div>
        <div className="flex flex-col gap-1.5">
          <span className="text-[13px] text-sec">Select, compact (32)</span>
          <Select aria-label="node compact" value={sel} onValueChange={setSel} options={options} compact />
        </div>
        <div className="flex flex-col gap-1.5">
          <span className="text-[13px] text-sec">Select, open (drawn: the menu look)</span>
          <div className="w-60 rounded-pop border border-line bg-surface p-1.5 shadow-(--shadow-pop)">
            {options.map((o, i) => (
              <div key={o.value} className={cx("flex min-h-10 items-center gap-2.5 rounded-ctl px-2.5 text-[13px]", i === 1 && "bg-step")}>
                <span className="flex-1">{o.label}</span>
                {i === 0 && <Icon name="check" size={14} className="text-accent-text" />}
              </div>
            ))}
          </div>
        </div>
      </Card>
    </div>
  );
}

function Controls() {
  const [on, setOn] = useState(true);
  const [c, setC] = useState(true);
  const [seg, setSeg] = useState("a");
  const [chip, setChip] = useState("all");
  const [tab, setTab] = useState("one");
  const [n, setN] = useState(100);
  const segOptions = [
    { value: "a", label: "Day" },
    { value: "b", label: "Week" },
    { value: "c", label: "Month" },
  ];
  return (
    <Card className="flex flex-col gap-5 p-5">
      <Group title="switch: off, on, disabled, focus (drawn)">
        <Switch aria-label="off" checked={false} onCheckedChange={() => {}} />
        <Switch aria-label="on" checked={on} onCheckedChange={setOn} />
        <Switch aria-label="disabled off" checked={false} disabled />
        <Switch aria-label="disabled on" checked disabled />
        <Switch aria-label="focus" checked={on} onCheckedChange={setOn} className={focusRing} />
      </Group>
      <Group title="check: off, on, with a label, disabled, focus (drawn)">
        <Check aria-label="off" checked={false} onCheckedChange={() => {}} />
        <Check aria-label="on" checked={c} onCheckedChange={(v) => setC(!!v)} />
        <Check label="With a label" checked={c} onCheckedChange={(v) => setC(!!v)} />
        <Check aria-label="disabled" checked={false} disabled />
        <Check aria-label="disabled on" checked disabled />
        <Check aria-label="focus" checked={false} onCheckedChange={() => {}} className={focusRing} />
      </Group>
      <Group title="segments: thumb, inset, flat">
        <Segmented variant="thumb" aria-label="thumb" value={seg} onValueChange={setSeg} options={segOptions} />
        <Segmented variant="inset" aria-label="inset" value={seg} onValueChange={setSeg} options={segOptions} />
        <Segmented variant="flat" aria-label="flat" value={seg} onValueChange={setSeg} options={segOptions} />
      </Group>
      <Group title="filter chips">
        <FilterChips
          aria-label="chips"
          value={chip}
          onValueChange={setChip}
          options={[
            { value: "all", label: "All" },
            { value: "warn", label: "Warnings" },
            { value: "bad", label: "Errors" },
          ]}
        />
      </Group>
      <Group title="stepper">
        <div className="w-44">
          <Stepper onDecrement={() => setN((x) => x - 10)} onIncrement={() => setN((x) => x + 10)} decrementLabel="less" incrementLabel="more">
            {n} GB
          </Stepper>
        </div>
      </Group>
      <div className="flex flex-col gap-2">
        <div className="text-xs text-muted">tabs 44, with a count</div>
        <Tabs
          aria-label="tabs"
          value={tab}
          onValueChange={setTab}
          items={[
            { value: "one", label: "Overview", content: <p className="text-sec">Overview content</p> },
            { value: "two", label: "Inbounds", badge: 3, content: <p className="text-sec">Inbounds content</p> },
            { value: "three", label: "History", content: <p className="text-sec">History content</p> },
            { value: "four", label: "Server", content: <p className="text-sec">Server content</p> },
          ]}
        />
      </div>
      <Group title="accent picker (changes the accent of the whole page)">
        <AccentPicker />
      </Group>
    </Card>
  );
}

function Statuses() {
  return (
    <Card className="flex flex-col gap-5 p-5">
      {([
        ["pill 32", {}],
        ["pill small 24", { sm: true }],
        ["pill in the bar (40)", { bar: true }],
        ["glyph only", { glyphOnly: true }],
      ] as const).map(([title, props]) => (
        <Group key={title} title={title}>
          {statusKinds.map((k) => (
            <StatusPill key={k} kind={k} {...props} />
          ))}
        </Group>
      ))}
      <Group title="dot and glyph">
        {statusKinds.map((k) => (
          <span key={k} className="flex items-center gap-2 text-[13px] text-sec">
            <StatusDot kind={k} />
            <StatusGlyph kind={k} />
            {k}
          </span>
        ))}
      </Group>
      <Group title="tag (24): plain, mono, one per tone">
        <Chip>plain</Chip>
        <Chip mono>203.0.113.7</Chip>
        {tones.map((t) => (
          <Chip key={t} tone={t}>
            {t}
          </Chip>
        ))}
      </Group>
      <Group title="protocol tags: full (24), compact (28, the name opens on hover or focus)">
        {protocols.map((p) => (
          <ProtocolTag key={p} protocol={p} label={protocolLabel[p]} />
        ))}
      </Group>
      <Group title="compact">
        {protocols.map((p) => (
          <ProtocolTag key={p} protocol={p} label={protocolLabel[p]} compact />
        ))}
      </Group>
      <Group title="keys, avatars, flags">
        <Kbd keys={["Ctrl", "K"]} />
        {[0, 1, 2, 3, 4, 5].map((i) => (
          <Avatar key={i} name={["Alice", "Boris", "Clara", "Denis", "Egor", "Anna"][i]!} index={i} />
        ))}
        <Avatar name="Alice" size={40} />
        {["DE", "NL", "FI", "RU", "FR"].map((c) => (
          <Flag key={c} code={c} />
        ))}
      </Group>
      <Group title="section icons, in their tones">
        {(Object.keys(navPaths) as NavIconName[]).map((n) => (
          <span key={n} className="flex items-center gap-1.5 text-[13px] text-sec">
            <NavIcon name={n} size={16} />
            {n}
          </span>
        ))}
      </Group>
    </Card>
  );
}

function Typography() {
  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Card className="flex flex-col gap-3 p-5">
        <PageTitle>Page title 28</PageTitle>
        <SectionLabel count={12}>Card heading with a count</SectionLabel>
        <SectionLabel icon="traffic" tone="sky" as="h3">
          Heading with a chip
        </SectionLabel>
        <SectionLabel caps>Editor section (capitals)</SectionLabel>
        <p className="text-sm leading-5">Body text 14/20 in the text colour.</p>
        <p className="text-sm leading-5 text-sec">Secondary text, a step quieter.</p>
        <p className="text-[13px] leading-5 text-muted">Muted: labels and hints, 13px.</p>
        <p className="font-mono text-[13px]" dir="ltr">
          203.0.113.7:443 · v0.1.99
        </p>
      </Card>
      <Card className="overflow-hidden">
        <div className="px-6 pt-4 pb-3">
          <SectionLabel count={3}>Nodes</SectionLabel>
        </div>
        <RowHead className="grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_auto]">
          <span>Name</span>
          <span>Protocols</span>
          <span>State</span>
        </RowHead>
        {[
          ["de1", "hysteria2", "ok"],
          ["nl1", "vless", "warn"],
          ["fi1", "awg", "bad"],
        ].map(([name, proto, kind]) => (
          <Row key={name} as="button" className="grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_auto]">
            <span className="flex items-center gap-3">
              <Flag code={name!.slice(0, 2)} />
              <b className="font-mono font-medium">{name}</b>
            </span>
            <ProtocolTag protocol={proto!} compact />
            <StatusPill kind={kind as StatusKind} sm />
          </Row>
        ))}
        <Row className="grid-cols-[1fr_auto]">
          <Skeleton className="w-32" />
          <Skeleton className="h-6 w-16 rounded-xl" />
        </Row>
      </Card>
    </div>
  );
}

function Feedback() {
  const toast = useToast();
  return (
    <div className="flex flex-col gap-4">
      <Card className="flex flex-col gap-3 p-5">
        <Notice title="Heads up.">An amber notice with a lead-in.</Notice>
        <Notice tone="danger" title="Failed.">
          A red notice.
        </Notice>
        <Notice tone="quiet">Nothing is wrong, it is just not here.</Notice>
        <Callout kind="warn" action={<Button size="sm">Fix</Button>}>
          A warning line inside a card, with an action.
        </Callout>
        <Callout kind="bad" alert>
          An error line inside a card.
        </Callout>
        {(["ok", "warn", "bad", "off", "busy", "blip"] as const).map((k) => (
          <Banner key={k} kind={k} title={`Banner: ${k}`} actions={<Button size="sm">Action</Button>}>
            A state banner under a page header, with a line of explanation.
          </Banner>
        ))}
      </Card>
      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <EmptyState icon={<Icon name="people" size={20} />} title="No people yet" action={<Button variant="primary">Add a person</Button>}>
            People you add show up here with their devices and traffic.
          </EmptyState>
        </Card>
        <Card className="flex flex-col gap-4 p-5">
          <QueryError error={new Error("boom")} onRetry={() => {}} />
          <QueryError error={new Error("boom")} onRetry={() => {}} compact />
          <Pending compact />
          <div className="flex flex-col gap-2">
            <Skeleton className="w-40" />
            <Skeleton className="w-24" />
          </div>
        </Card>
      </div>
      <Card className="flex flex-wrap items-center gap-2 p-5">
        <Button onClick={() => toast("Saved")}>Toast: a note</Button>
        <Button onClick={() => toast("User removed", { undo: () => {} })}>Toast: with Undo</Button>
        <Button onClick={() => toast.error("The node is not connected")}>Toast: an error</Button>
      </Card>
    </div>
  );
}

function Overlays() {
  const initial = new URLSearchParams(location.search).get("open");
  const [modal, setModal] = useState<"" | "narrow" | "wide">(initial === "modal" ? "narrow" : initial === "wide" ? "wide" : "");
  const [pop, setPop] = useState(initial === "popover");
  return (
    <Card className="flex flex-wrap items-center gap-3 p-5">
      <Button onClick={() => setModal("narrow")}>Window 520</Button>
      <Button onClick={() => setModal("wide")}>Window 760</Button>
      <Popover
        open={pop}
        onOpenChange={setPop}
        trigger={
          <Button>
            <StatusDot kind="ok" />
            Fleet
          </Button>
        }
      >
        {[
          ["de1", "hysteria2", "ok"],
          ["nl1", "vless", "warn"],
          ["fi1", "awg", "ok"],
        ].map(([name, proto, kind]) => (
          <PopRow key={name} className="grid-cols-[24px_44px_minmax(0,1fr)_auto]">
            <Flag code={name!.slice(0, 2)} />
            <b className="font-mono font-medium">{name}</b>
            <ProtocolTag protocol={proto!} compact />
            <StatusPill kind={kind as StatusKind} sm />
          </PopRow>
        ))}
        <PopFooter href="#">
          All nodes <Icon name="chevronRight" size={14} />
        </PopFooter>
      </Popover>
      <Tip label="A tooltip on hover or focus">
        <Button variant="ghost">Tooltip</Button>
      </Tip>
      <Modal
        open={modal !== ""}
        onOpenChange={(o) => !o && setModal("")}
        wide={modal === "wide"}
        closeLabel="Close"
        title="Add a person"
        description="They get a link to the subscription page."
        footer={
          <>
            <Button variant="ghost" onClick={() => setModal("")}>
              Cancel
            </Button>
            <Button variant="primary" onClick={() => setModal("")}>
              Add
            </Button>
          </>
        }
      >
        <TextField label="Name" placeholder="alice" />
        <div className="grid gap-3 sm:grid-cols-2">
          <TextField label="Traffic limit" placeholder="100 GB" />
          <TextField label="Devices" placeholder="5" hint="At most" />
        </div>
        <Check label="Send the link by mail" checked onCheckedChange={() => {}} />
      </Modal>
    </Card>
  );
}

/** The base components of the redesign (stage 1), every variant and state; see the comment at the top of the file. */
export function FoundationKit() {
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Foundation</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      <Shot id="fnd-tokens" title="Colours (names and what the browser resolved them to)">
        <Tokens />
      </Shot>
      <Shot id="fnd-buttons" title="Buttons">
        <Buttons />
      </Shot>
      <Shot id="fnd-fields" title="Fields, textareas, selects">
        <Fields />
      </Shot>
      <Shot id="fnd-controls" title="Switches, checks, segments, chips, stepper, tabs">
        <Controls />
      </Shot>
      <Shot id="fnd-status" title="Status, tags, protocols, avatars, flags, icons">
        <Statuses />
      </Shot>
      <Shot id="fnd-type" title="Type and rows">
        <Typography />
      </Shot>
      <Shot id="fnd-feedback" title="Notices, banners, empty and error states, toasts">
        <Feedback />
      </Shot>
      <Shot id="fnd-overlays" title="Windows, popover, tooltip">
        <Overlays />
      </Shot>
    </div>
  );
}
