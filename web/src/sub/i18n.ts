import type { Lang, LoadLevel } from "./types";

// Copy of the user page: the end user is addressed formally («вы» / "you").
// Functions take plain values; dates and plurals come from Intl, so this file is only words. No app is named here: names
// come from the settings (the cards) and the page speaks of what an app does, not of which one it is.
//
// The two ways to connect have one vocabulary on the page and in the admin: «Подписка» (subscription apps: one link
// with every server) and «Ключ» (AmneziaVPN: one per device).

type Period = "none" | "day" | "week" | "month" | "rolling_month";

export type Dict = {
  hi: (n: string) => string;
  hiAnon: string;
  traffic: string;
  leftL: string;
  termL: string;
  trafficL: string;
  resets: (d: string) => string;
  unlimited: string;
  noExpiry: string;
  of: (n: string) => string;
  until: (d: string) => string;
  chip: Record<"active" | "expired" | "limited" | "disabled", string>;
  chipLong: Record<"active" | "expired" | "limited" | "disabled", string>;
  /** [what happened, what to do]: the second sentence is dropped when there is no support link. */
  txt: {
    expired: (brand: string) => [string, string];
    limitedReset: (date: string, period: Period) => [string, string];
    limited: () => [string, string];
    disabled: () => [string, string];
  };
  recommended: string;
  noApps: string;
  // the two ways and the platform they are for
  pickDev: string;
  yourDevice: string;
  twoWays: string;
  linkT: (apps: string) => string;
  linkD: string;
  keyT: (app: string) => string;
  keyD: string;
  stepInstall: (app: string) => string;
  stepAddSub: string;
  stepAddDev: string;
  stepAskKey: string;
  download: string;
  store: { appStore: string; play: string; site: string };
  addTo: (app: string) => string;
  noOpen: string;
  pasteHow: (app: string) => string;
  copyHow: (app: string) => string;
  allServers: (app: string, n: number) => string;
  otherApps: string;
  noAppHere: (platform: string) => string;
  copyLink: string;
  copied: string;
  copiedShort: string;
  qrOther: string;
  qrPhoneT: string;
  /** "Point the phone's camera…"; the argument is "" when the settings name no phone app. */
  qrHow: (app: string) => string;
  viaLink: string;
  serverLoadTitle: string;
  serverLoadIntro: string;
  serverLoadBusy: string;
  serverLoadTry: (busy: string, other: string) => string;
  serverLoadLevel: Record<LoadLevel, string>;
  linkApps: string;
  linkAppsNote: string;
  fetched: (when: string) => string;
  devApp: (app: string) => string;
  yourKeys: string;
  keysNone: string;
  used: (a: number, b: number) => string;
  limit: (a: number, b: number, canRemove: boolean) => string;
  keysByAdmin: string;
  noProfile: string;
  // the password of the page
  lockT: string;
  lockH: string;
  lockLabel: string;
  lockGo: string;
  lockBusy: string;
  lockWrong: (left: number) => string;
  lockLater: (min: number) => string;
  lockNet: string;
  lockFail: string;
  lockLatin: string;
  lockShort: (n: number) => string;
  lockNone: string;
  // the key devices (self-service)
  awgAdd: string;
  awgAddT: string;
  awgAddH: string;
  awgReadyT: (name: string) => string;
  awgReadyH: string;
  renewT: (name: string) => string;
  renewH: (app: string) => string;
  /** What to delete in the app: the device's old connection. */
  renewOld: (app: string) => string;
  awgDone: string;
  profile: string;
  profileMain: string;
  profileOld: (app: string) => string;
  profileWarp: string;
  awgPlatform: string;
  awgName: string;
  /** An example name per device kind, the placeholder of the name field. */
  awgNamePh: Record<"ios" | "android" | "windows" | "macos" | "linux" | "other", string>;
  awgCreate: string;
  awgCancel: string;
  awgBusy: string;
  showKey: string;
  hideKey: string;
  rotateKey: string;
  removeKey: string;
  removeQ: (name: string) => string;
  rotateQ: (name: string, app: string) => string;
  removeYes: string;
  rotateYes: string;
  awgStale: string;
  awgHandshake: (when: string) => string;
  awgNever: string;
  // the key itself
  country: string;
  countryH: string;
  copyKey: string;
  keyCopied: string;
  downloadFile: string;
  phoneSteps: (app: string) => string[];
  desktopSteps: (app: string) => string;
  desktopFileOnly: (app: string) => string;
  qrOtherDev: string;
  qrScan: (app: string) => string;
  qrHowKey: (app: string) => string;
  openThere: string;
  qrTooBig: string;
  needApp: (list: { app: string; min: string }[]) => string;
  awgSecret: string;
  err: (code: string, retryMin: number) => string;
  // "new key needed"
  staleT: (name: string) => string;
  staleTs: string;
  staleD: string;
  staleSteps: (app: string) => string[];
  newKey: string;
  newKeyFor: (name: string) => string;
  devGeneric: string;
  online: string;
  help: string;
  helpTg: string;
  helpAny: string;
  write: string;
  writeSupport: string;
  writeWeb: (tg: boolean) => string;
  privacy: string;
  langLabel: string;
  close: string;
  platforms: Record<"ios" | "android" | "windows" | "macos" | "linux" | "other", string>;
};

const ruPeriod: Record<Period, string> = { none: "", day: "на сегодня", week: "на эту неделю", month: "на этот месяц", rolling_month: "на этот месяц" };
const enPeriod: Record<Period, string> = { none: "", day: "for today", week: "for this week", month: "for this month", rolling_month: "for this month" };

const ru: Dict = {
  hi: (n) => `Привет, ${n}`,
  hiAnon: "Привет",
  traffic: "Трафик",
  leftL: "ОСТАЛОСЬ",
  termL: "СРОК",
  trafficL: "ТРАФИК",
  resets: (d) => `обнулится ${d}`,
  unlimited: "без лимита",
  noExpiry: "без срока",
  of: (n) => `из ${n}`,
  until: (d) => `до ${d}`,
  chip: { active: "Активна", expired: "Закончилась", limited: "Трафик исчерпан", disabled: "Отключена" },
  chipLong: {
    active: "Подписка активна",
    expired: "Подписка закончилась",
    limited: "Трафик закончился",
    disabled: "Подписка отключена",
  },
  txt: {
    expired: (b) => [`Интернет через ${b} сейчас не работает.`, "Напишите — продлим."],
    limitedReset: (d, p) => [`Трафик ${ruPeriod[p]} закончился — до ${d} VPN не работает.`.replace("  ", " "), "Нужно раньше — напишите."],
    limited: () => ["Лимит трафика исчерпан.", "Напишите — увеличим."],
    disabled: () => ["Администратор приостановил доступ.", "Если это ошибка — напишите."],
  },
  recommended: "рекомендуем",
  noApps: "Сервер ещё настраивается — напишите администратору.",
  pickDev: "На чём будете пользоваться",
  yourDevice: "Ваше устройство",
  twoWays: "Подойдёт любой из двух способов — или оба сразу.",
  linkT: (a) => (a ? `Подписка — ${a}` : "Подписка"),
  linkD: "Одна личная ссылка со всеми вашими серверами — обновляется сама",
  keyT: (a) => `Ключ ${a}`,
  keyD: "Отдельный ключ для каждого телефона и компьютера",
  stepInstall: (a) => `Установите ${a}`,
  stepAddSub: "Добавьте подписку",
  stepAddDev: "Добавьте устройство — для него появится ключ",
  stepAskKey: "Попросите ключ",
  download: "Скачать",
  store: { appStore: "App Store", play: "Google Play", site: "с сайта" },
  addTo: (a) => `Добавить в ${a}`,
  noOpen: "Не открывается? Скопировать ссылку",
  pasteHow: (a) => `В ${a}: «+» → «Вставить из буфера»`,
  copyHow: (a) => `В ${a}: добавить подписку → вставить ссылку`,
  allServers: (a, n) => `В ${a} появятся все ваши серверы (${n}) — выберите любой`,
  otherApps: "Другие приложения",
  noAppHere: (p) => `Для ${p} приложения нет — выберите другое устройство выше.`,
  copyLink: "Скопировать ссылку",
  copied: "Ссылка скопирована ✓",
  copiedShort: "Скопировано",
  qrOther: "Подключить другое устройство (QR‑код)",
  qrPhoneT: "Подключить телефон",
  qrHow: (a) => (a ? `Наведите камеру телефона — откроется эта страница. Или в ${a}: «+» → «Сканировать QR»` : "Наведите камеру телефона — откроется эта страница."),
  viaLink: "Подключено через подписку",
  serverLoadTitle: "Загрузка каналов",
  serverLoadIntro: "Насколько занят канал каждого сервера прямо сейчас. Показаны серверы, для которых известна пропускная способность.",
  serverLoadBusy: "Высокая загрузка канала",
  serverLoadTry: (busy, other) => `Канал сервера ${busy} сильно загружен. Если соединение медленное, попробуйте ${other}.`,
  serverLoadLevel: { low: "Низкая", medium: "Средняя", high: "Высокая" },
  linkApps: "Приложения по ссылке",
  linkAppsNote: "Все устройства с этой ссылкой занимают одно место",
  fetched: (w) => `обновлялись ${w}`,
  devApp: (a) => `Устройство ${a}`,
  yourKeys: "Ваши ключи",
  keysNone: "Пока ни одного — добавьте первое устройство.",
  used: (a, b) => `занято ${a} из ${b}`,
  limit: (a, b, rm) =>
    rm ? `Занято ${a} из ${b}. Удалите устройство, которым больше не пользуетесь (кнопка «Удалить» в списке ниже), или напишите — добавим место.` : `Занято ${a} из ${b}. Напишите — добавим место.`,
  keysByAdmin: "Ключи выдаёт администратор — напишите, если нужен новый.",
  noProfile: "Сервер ещё настраивается — напишите администратору.",
  lockT: "Введите пароль",
  lockH: "Его прислали вместе со ссылкой. Спросим один раз — в этом браузере запомним.",
  lockLabel: "Пароль",
  lockGo: "Открыть",
  lockBusy: "Проверяем…",
  lockWrong: (n) => (n > 0 ? `Пароль не подошёл. Осталось попыток: ${n}.` : "Пароль не подошёл."),
  lockLater: (m) => (m > 0 ? `Слишком много попыток. Попробуйте через ${m} мин.` : "Слишком много попыток. Попробуйте позже."),
  lockNet: "Нет связи. Проверьте интернет и повторите.",
  lockFail: "Не получилось. Попробуйте ещё раз.",
  lockLatin: "Пароль набирается латиницей — переключите клавиатуру на English (кнопка 🌐).",
  lockShort: (n) => `В пароле 8 знаков, вы ввели ${n}.`,
  lockNone: "Нет пароля? Спросите у того, кто прислал ссылку.",
  awgAdd: "Добавить устройство",
  awgAddT: "Новое устройство",
  awgAddH: "Для каждого телефона и компьютера нужен свой ключ. Он появится сразу после создания.",
  awgReadyT: (n) => `Ключ для «${n}»`,
  awgReadyH: "Настройте устройство сейчас. Закроете окно — устройство останется в списке, ключ можно будет показать снова.",
  renewT: (n) => `Новый ключ для «${n}»`,
  renewH: (a) => `Добавьте этот ключ в ${a}, как в первый раз.`,
  renewOld: (a) => `Старое подключение этого устройства в ${a} удалите — оно больше не работает.`,
  awgDone: "Готово",
  profile: "Вариант подключения",
  profileMain: "Основной",
  profileOld: (a) => `Для старых версий ${a} (до 5.0.1.5)`,
  profileWarp: "Запасной выход (если какой-то сайт не открывается)",
  awgPlatform: "Что за устройство",
  awgName: "Название (необязательно)",
  awgNamePh: {
    ios: "Например, телефон мамы",
    android: "Например, рабочий телефон",
    windows: "Например, домашний ПК",
    macos: "Например, MacBook Air",
    linux: "Например, ноутбук",
    other: "Например, планшет",
  },
  awgCreate: "Создать",
  awgCancel: "Отмена",
  awgBusy: "Секунду…",
  showKey: "Показать ключ",
  hideKey: "Скрыть ключ",
  rotateKey: "Заменить ключ",
  removeKey: "Удалить",
  removeQ: (n) => `Удалить «${n}»? VPN на нём сразу перестанет работать.`,
  rotateQ: (n, a) => `Заменить ключ «${n}»? Старый перестанет работать сразу — новый нужно будет добавить в ${a} заново.`,
  removeYes: "Удалить",
  rotateYes: "Заменить",
  awgStale: "нужен новый ключ",
  awgHandshake: (w) => `подключалось ${w}`,
  awgNever: "ещё не подключалось",
  country: "Страна",
  countryH: "Каждая страна — отдельное подключение. Добавьте одну; перестанет работать — добавьте другую.",  copyKey: "Скопировать ключ",
  keyCopied: "Ключ скопирован ✓",
  downloadFile: "Скачать файл",
  phoneSteps: (a) => [`Откройте ${a}`, "Нажмите «+» и вставьте ключ", "Нажмите «Продолжить»"],
  desktopSteps: (a) => `${a} → «+» → «Файл с настройками подключения» → выберите скачанный файл`,
  desktopFileOnly: (a) => `На компьютере добавляйте в ${a} файл, а не ключ: с ключом соединение может не заработать.`,
  qrOtherDev: "QR‑код для другого устройства",
  qrScan: (a) => `Отсканируйте в ${a}`,
  qrHowKey: (a) => `В ${a}: «+» → «QR-код»`,
  openThere: "Проще всего открыть эту страницу на том компьютере и нажать «Показать ключ» там.",
  qrTooBig: "Ключ слишком длинный для QR-кода. Скачайте файл или скопируйте ключ.",
  needApp: (l) => `Нужна ${l.map((m, i) => `${m.app} ${m.min} ${i === 0 ? "или новее" : "и новее"}`).join(" — или ")}`,
  awgSecret: "В ключе личные данные устройства — не пересылайте его.",
  err: (c, m) =>
    (
      {
        device_limit: "Все места заняты. Удалите устройство, которым больше не пользуетесь, или напишите — добавим место.",
        user_inactive: "Подписка сейчас не активна, ключи не выдаются.",
        user_disabled: "Подписка отключена, ключи не выдаются.",
        app_disabled: "Это приложение для вас отключено.",
        profile_not_in_group: "Этот вариант подключения вам недоступен — напишите в поддержку.",
        no_inbound: "Этот вариант пока нигде не запущен — напишите в поддержку.",
        subnet_full: "На этом сервере закончились адреса. Напишите в поддержку.",
        agent_too_old: "Сервер ещё обновляется. Попробуйте позже.",
        self_service_disabled: "Ключи выдаёт администратор — напишите, если нужен новый.",
        too_many_requests: m > 0 ? `Слишком много действий подряд. Попробуйте через ${m} мин.` : "Слишком много действий подряд. Попробуйте позже.",
        not_found: "Такого устройства уже нет. Обновите страницу.",
        network: "Нет связи. Проверьте интернет и повторите.",
        locked: "Нужен пароль. Обновите страницу.",
      } as Record<string, string>
    )[c] ?? "Не получилось. Попробуйте ещё раз.",
  staleT: (n) => `Нужен новый ключ для «${n}»`,
  staleTs: "Нужны новые ключи",
  staleD: "Серверы обновились, и старое подключение перестало работать. Займёт минуту:",
  staleSteps: (a) => ["Нажмите «Получить новый ключ».", `Добавьте его в ${a}, как в первый раз.`, "Удалите старое подключение этого устройства — оно больше не работает."],
  newKey: "Получить новый ключ",
  newKeyFor: (n) => `Новый ключ для «${n}»`,
  devGeneric: "Устройство",
  online: "в сети",
  help: "Что-то не работает?",
  helpTg: "Ответим в Telegram",
  helpAny: "Поможем",
  write: "Написать",
  writeSupport: "Написать в поддержку",
  writeWeb: (tg) => (tg ? "Написать в Telegram →" : "Написать в поддержку →"),
  privacy: "Ссылка личная — не пересылайте её. Работает, пока подписка активна.",
  langLabel: "Язык: русский. Переключить на English",
  close: "Закрыть",
  platforms: { ios: "iPhone", android: "Android", windows: "Windows", macos: "Mac", linux: "Linux", other: "Другое" },
};

const en: Dict = {
  hi: (n) => `Hi, ${n}`,
  hiAnon: "Hi",
  traffic: "Traffic",
  leftL: "LEFT",
  termL: "TERM",
  trafficL: "TRAFFIC",
  resets: (d) => `resets on ${d}`,
  unlimited: "unlimited",
  noExpiry: "no expiry",
  of: (n) => `of ${n}`,
  until: (d) => `until ${d}`,
  chip: { active: "Active", expired: "Ended", limited: "Traffic used up", disabled: "Disabled" },
  chipLong: {
    active: "Subscription active",
    expired: "Subscription ended",
    limited: "Traffic used up",
    disabled: "Subscription disabled",
  },
  txt: {
    expired: (b) => [`Internet via ${b} is off right now.`, "Message us to renew."],
    limitedReset: (d, p) => [`The traffic ${enPeriod[p]} is used up — the VPN is off until ${d}.`.replace("  ", " "), "Need it sooner — message us."],
    limited: () => ["The traffic limit is used up.", "Message us to raise it."],
    disabled: () => ["The admin paused access.", "If that’s a mistake — message us."],
  },
  recommended: "recommended",
  noApps: "The server is still being set up — message the admin.",
  pickDev: "What you’ll use it on",
  yourDevice: "Your device",
  twoWays: "Either of the two ways will do — or both at once.",
  linkT: (a) => (a ? `Subscription — ${a}` : "Subscription"),
  linkD: "One personal link with all your servers — it updates itself",
  keyT: (a) => `${a} key`,
  keyD: "A separate key for each phone and computer",
  stepInstall: (a) => `Install ${a}`,
  stepAddSub: "Add the subscription",
  stepAddDev: "Add the device — it gets its own key",
  stepAskKey: "Ask for a key",
  download: "Download",
  store: { appStore: "App Store", play: "Google Play", site: "website" },
  addTo: (a) => `Add to ${a}`,
  noOpen: "Won’t open? Copy the link",
  pasteHow: (a) => `In ${a}: “+” → “Paste from clipboard”`,
  copyHow: (a) => `In ${a}: add a subscription → paste the link`,
  allServers: (a, n) => `All your servers (${n}) appear in ${a} — pick any`,
  otherApps: "Other apps",
  noAppHere: (p) => `There’s no app for ${p} — pick another device above.`,
  copyLink: "Copy link",
  copied: "Link copied ✓",
  copiedShort: "Copied",
  qrOther: "Connect another device (QR code)",
  qrPhoneT: "Connect a phone",
  qrHow: (a) => (a ? `Point the phone’s camera at it — this page opens. Or in ${a}: “+” → “Scan QR”` : "Point the phone’s camera at it — this page opens."),
  viaLink: "Connected with the subscription",
  serverLoadTitle: "Channel utilization",
  serverLoadIntro: "How busy each server’s channel is right now. Only servers with a known capacity are shown.",
  serverLoadBusy: "High channel utilization",
  serverLoadTry: (busy, other) => `The ${busy} server’s channel is very busy. If your connection is slow, try ${other}.`,
  serverLoadLevel: { low: "Low", medium: "Medium", high: "High" },
  linkApps: "Apps on the link",
  linkAppsNote: "All devices on this link take one slot",
  fetched: (w) => `updated ${w}`,
  devApp: (a) => `${a} device`,
  yourKeys: "Your keys",
  keysNone: "None yet — add your first device.",
  used: (a, b) => `${a} of ${b} used`,
  limit: (a, b, rm) =>
    rm ? `${a} of ${b} used. Remove a device you no longer use (the “Remove” button in the list below), or message us — we’ll add a slot.` : `${a} of ${b} used. Message us — we’ll add a slot.`,
  keysByAdmin: "Keys come from the admin — message us if you need a new one.",
  noProfile: "The server is still being set up — message the admin.",
  lockT: "Enter the password",
  lockH: "It came with your link. We ask once — this browser remembers it.",
  lockLabel: "Password",
  lockGo: "Open",
  lockBusy: "Checking…",
  lockWrong: (n) => (n > 0 ? `That password didn’t match. Tries left: ${n}.` : "That password didn’t match."),
  lockLater: (m) => (m > 0 ? `Too many tries. Try again in ${m} min.` : "Too many tries. Try again later."),
  lockNet: "No connection. Check the internet and try again.",
  lockFail: "That didn’t work. Try again.",
  lockLatin: "The password is in Latin letters — switch the keyboard to English (the 🌐 key).",
  lockShort: (n) => `The password has 8 characters, you typed ${n}.`,
  lockNone: "No password? Ask the person who sent you the link.",
  awgAdd: "Add a device",
  awgAddT: "New device",
  awgAddH: "Each phone and computer needs its own key. It appears right after you create the device.",
  awgReadyT: (n) => `Key for “${n}”`,
  awgReadyH: "Set the device up now. If you close this window the device stays in your list and the key can be shown again.",
  renewT: (n) => `New key for “${n}”`,
  renewH: (a) => `Add this key to ${a} like the first time.`,
  renewOld: (a) => `Delete this device’s old connection in ${a} — it no longer works.`,
  awgDone: "Done",
  profile: "Connection option",
  profileMain: "Main",
  profileOld: (a) => `For old ${a} versions (before 5.0.1.5)`,
  profileWarp: "Spare exit (if some site won’t open)",
  awgPlatform: "What kind of device",
  awgName: "Name (optional)",
  awgNamePh: {
    ios: "e.g. Mum's phone",
    android: "e.g. work phone",
    windows: "e.g. home PC",
    macos: "e.g. MacBook Air",
    linux: "e.g. laptop",
    other: "e.g. tablet",
  },
  awgCreate: "Create",
  awgCancel: "Cancel",
  awgBusy: "One moment…",
  showKey: "Show key",
  hideKey: "Hide key",
  rotateKey: "Replace key",
  removeKey: "Remove",
  removeQ: (n) => `Remove “${n}”? The VPN on it stops working at once.`,
  rotateQ: (n, a) => `Replace the key of “${n}”? The old one stops working at once — you’ll add the new one to ${a} again.`,
  removeYes: "Remove",
  rotateYes: "Replace",
  awgStale: "new key needed",
  awgHandshake: (w) => `connected ${w}`,
  awgNever: "not connected yet",
  country: "Country",
  countryH: "Each country is a separate connection. Add one; if it stops working, add another.",  copyKey: "Copy key",
  keyCopied: "Key copied ✓",
  downloadFile: "Download file",
  phoneSteps: (a) => [`Open ${a}`, "Tap “+” and paste the key", "Tap “Continue”"],
  desktopSteps: (a) => `${a} → “+” → “Connection settings file” → pick the downloaded file`,
  desktopFileOnly: (a) => `On a computer, add the file to ${a}, not the key: with the key the connection may not work.`,
  qrOtherDev: "QR code for another device",
  qrScan: (a) => `Scan it in ${a}`,
  qrHowKey: (a) => `In ${a}: “+” → “QR code”`,
  openThere: "Easiest: open this page on that computer and press “Show key” there.",
  qrTooBig: "The key is too long for a QR code. Download the file or copy the key.",
  needApp: (l) => `Needs ${l.map((m, i) => `${m.app} ${m.min} ${i === 0 ? "or newer" : "and newer"}`).join(" — or ")}`,
  awgSecret: "The key holds this device’s private data — don’t share it.",
  err: (c, m) =>
    (
      {
        device_limit: "All slots are used. Remove a device you no longer use, or message us — we’ll add a slot.",
        user_inactive: "The subscription isn’t active, so no keys are issued.",
        user_disabled: "The subscription is disabled, so no keys are issued.",
        app_disabled: "This app is turned off for you.",
        profile_not_in_group: "That connection option isn’t available to you — message support.",
        no_inbound: "That connection option isn’t running anywhere yet — message support.",
        subnet_full: "That server ran out of addresses. Message support.",
        agent_too_old: "The server is still updating. Try again later.",
        self_service_disabled: "Keys come from the admin — message us if you need a new one.",
        too_many_requests: m > 0 ? `Too many actions in a row. Try again in ${m} min.` : "Too many actions in a row. Try again later.",
        not_found: "That device is already gone. Refresh the page.",
        network: "No connection. Check the internet and try again.",
        locked: "A password is needed. Refresh the page.",
      } as Record<string, string>
    )[c] ?? "That didn’t work. Try again.",
  staleT: (n) => `New key needed for “${n}”`,
  staleTs: "New keys needed",
  staleD: "The servers were updated and the old connection stopped working. Takes a minute:",
  staleSteps: (a) => ["Press “Get a new key”.", `Add it to ${a} like the first time.`, "Delete this device’s old connection — it no longer works."],
  newKey: "Get a new key",
  newKeyFor: (n) => `New key for “${n}”`,
  devGeneric: "Device",
  online: "online",
  help: "Something not working?",
  helpTg: "We reply in Telegram",
  helpAny: "We can help",
  write: "Message",
  writeSupport: "Message support",
  writeWeb: (tg) => (tg ? "Message on Telegram →" : "Message support →"),
  privacy: "This link is personal — don’t share it. Works while the subscription is active.",
  langLabel: "Language: English. Switch to Russian",
  close: "Close",
  platforms: { ios: "iPhone", android: "Android", windows: "Windows", macos: "Mac", linux: "Linux", other: "Other" },
};

// A dash never starts a line: the space before it does not break (U+00A0). Applied to every string of the dictionary and
// to whatever its functions return.
function typeset<T>(v: T): T {
  if (typeof v === "string") return v.replace(/ —/g, " —") as T;
  if (typeof v === "function") return ((...args: unknown[]) => typeset((v as (...a: unknown[]) => unknown)(...args))) as T;
  if (Array.isArray(v)) return v.map(typeset) as T;
  if (v && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, typeset(x)])) as T;
  return v;
}

export const dict: Record<Lang, Dict> = { ru: typeset(ru), en: typeset(en) };
