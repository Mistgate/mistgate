import type { Lang } from "./types";

// dlg: words of the small windows of the redesigned page (the device menu, rename, delete and replace, the question of a stale
// key, the DNS choice and its result, the password form). Keys are added to `ru` and every other language must have them (the
// type is `ru`'s). Functions take plain values. The user is addressed formally («вы»), as on the boards (V2Dlg*). What the
// boards already say with a word of i18n.ts (Close, Cancel, Save, Done, Rename, Replace key, Get a new key) is not repeated here.

/** Names (a device, an app, a server) inside Persian text: kept in their own direction. */
const iso = (s: string) => (s ? `${/[؀-ۿ]/.test(s) ? "⁧" : "⁨"}${s}⁩` : s);

/** The board writes the description of a preset after the name in small letters ("AdGuard, без рекламы"): Russian words only (a Latin name keeps its capital). */
const small = (s: string) => (/^[А-ЯЁ][а-яё]/.test(s) ? `${s[0]!.toLowerCase()}${s.slice(1)}` : s);

/**
 * "Германия" -> "Германии": the boards say "Ключ для Германии" and the page only has the nominative. One word of a country is
 * declined by its ending; an acronym stays; what the rules do not know (a name of several words, "Беларусь") is null.
 */
export function genitive(w: string): string | null {
  if (!/^[А-ЯЁ][а-яё]+$/.test(w)) return /^[А-ЯЁ]{2,}$/.test(w) ? w : null;
  if (/ия$/.test(w)) return `${w.slice(0, -1)}и`;
  if (/[гкхжчшщ]а$/.test(w)) return `${w.slice(0, -1)}и`;
  if (/[ая]$/.test(w)) return `${w.slice(0, -1)}${w.endsWith("а") ? "ы" : "и"}`;
  if (/[бвгджзклмнпрстфхцчшщ]ы$/.test(w)) return `${w.slice(0, -1)}ов`;
  if (/й$/.test(w)) return `${w.slice(0, -1)}я`;
  if (/[бвгджзклмнпрстфхцчшщ]$/.test(w)) return `${w}а`;
  return null;
}

const ru = {
  // the menu of a device, rename, delete, replace
  dgMenuAria: "Действия с устройством",
  dgDelete: "Удалить устройство",
  dgDelT: (name: string) => `Удалить ${name}`,
  dgDelD: "Ключ этого устройства перестанет работать, а место освободится. Вернуть устройство можно будет только новым ключом",
  dgDelLinkD: "Приложение на этом устройстве перестанет подключаться, а место освободится. Вернуть устройство можно будет, заново добавив подписку в приложении",
  dgRotT: (name: string) => `Заменить ключ ${name}`,
  dgRotD: (app: string) => `Старый ключ перестанет работать сразу, а новый нужно будет добавить в ${app} заново`,
  dgRotBtn: "Заменить",
  dgNameMax: "до 40 знаков",
  // the question of a stale key
  dgStaleT: (name: string) => `Нужен новый ключ для ${name}`,
  dgStaleD: "Серверы обновились, и старый ключ этого устройства больше не работает",
  dgStaleDnsD: "DNS сервера изменился — получите ключ заново. Сам ключ останется прежним",
  dgStaleSteps: (app: string) => ["Нажмите «Получить новый ключ»", `Добавьте его в ${app}, как в первый раз`, `Удалите в ${app} старое подключение этого устройства`],
  dgLater: "Позже",
  // the DNS choice
  dgDnsT: "DNS для ключей",
  dgDnsLead: (app: string) => `Действует для ключей ${app}. В приложениях по ссылке DNS пока один на все серверы`,
  dgDnsScope: "Для каких серверов",
  dgDnsAll: "Все серверы",
  dgDnsOne: (place: string) => `Только ${place}`,
  dgDnsField: "DNS",
  dgDnsHint: "После смены ключи нужно получить заново",
  dgLinkLead: (hours: number) => `Приложения получат новый DNS при обновлении подписки (до ${hours} ч)`,
  // "DNS changed"
  dgDoneT: "DNS изменён",
  dgDoneAll: (name: string, desc: string) => `Теперь на всех серверах ${name}${desc ? `, ${small(desc)}` : ""}. Сами ключи остались прежними, но каждое устройство с ключом должно получить его заново`,
  dgDoneOne: (place: string, name: string, desc: string) => `Теперь на сервере ${place} ${name}${desc ? `, ${small(desc)}` : ""}. Сам ключ остался прежним, но каждое устройство с ключом должно получить его заново`,
  dgDoneNoneAll: "DNS сохранён — его получат новые ключи",
  dgDoneNoneOne: "DNS сохранён — его получат новые ключи этого сервера",
  dgDoneLink: (hours: number) => `Приложения по ссылке получат новый DNS при обновлении подписки (до ${hours} ч). Чтобы сразу — обновите подписку в приложении`,
  dgDoneSkip: (names: string) => `Этого варианта нет не на всех серверах: ${names} остались как были`,
  dgDoneRow: (country: string) => {
    const g = genitive(country);
    return g ? `Ключ для ${g} нужно получить заново` : `${country}: ключ нужно получить заново`;
  },
  dgDoneRowNo: "Ключ нужно получить заново",
  dgGet: "Получить",
  dgGetAria: (name: string) => `Получить новый ключ для ${name}`,
  // the password form
  dgLockT: "Страница закрыта паролем",
  dgLockH: "Его задал владелец подписки",
  dgLockWrong: (left: number) => `Пароль не подошёл, проверьте раскладку и попробуйте ещё раз${left > 0 ? `. Осталось попыток: ${left}` : ""}`,
};

export type DlgDict = typeof ru;

const en: DlgDict = {
  dgMenuAria: "Device actions",
  dgDelete: "Remove device",
  dgDelT: (name) => `Remove ${name}`,
  dgDelD: "This device’s key will stop working and its place will be freed. The device can only come back with a new key",
  dgDelLinkD: "The app on this device will stop connecting and its place will be freed. To bring the device back, add the subscription in the app again",
  dgRotT: (name) => `Replace the key of ${name}`,
  dgRotD: (app) => `The old key stops working at once, and you’ll add the new one to ${app} again`,
  dgRotBtn: "Replace",
  dgNameMax: "up to 40 characters",
  dgStaleT: (name) => `New key needed for ${name}`,
  dgStaleD: "The servers were updated, and this device’s old key no longer works",
  dgStaleDnsD: "A server’s DNS changed — get the key again. The key itself stays the same",
  dgStaleSteps: (app) => ["Press “Get a new key”", `Add it to ${app} like the first time`, `Delete this device’s old connection in ${app}`],
  dgLater: "Later",
  dgDnsT: "DNS for keys",
  dgDnsLead: (app) => `Applies to ${app} keys. In apps that use the link, DNS is still one for all servers`,
  dgDnsScope: "Which servers",
  dgDnsAll: "All servers",
  dgDnsOne: (place) => `Only ${place}`,
  dgDnsField: "DNS",
  dgDnsHint: "After the change, keys must be fetched again",
  dgLinkLead: (hours) => `The apps get the new DNS when they update the subscription (within ${hours} h)`,
  dgDoneT: "DNS changed",
  dgDoneAll: (name, desc) => `Now all servers use ${name}${desc ? ` (${desc})` : ""}. The keys themselves stay the same, but each device with a key must get it again`,
  dgDoneOne: (place, name, desc) => `Now the server ${place} uses ${name}${desc ? ` (${desc})` : ""}. The key itself stays the same, but each device with a key must get it again`,
  dgDoneNoneAll: "DNS saved — new keys get it",
  dgDoneNoneOne: "DNS saved — new keys on this server get it",
  dgDoneLink: (hours) => `Apps that use the link get the new DNS when they update the subscription (within ${hours} h). To get it now, update the subscription in the app`,
  dgDoneSkip: (names) => `Not every server has this option: ${names} stayed as they were`,
  dgDoneRow: (country) => `The key for ${country} must be fetched again`,
  dgDoneRowNo: "The key must be fetched again",
  dgGet: "Get",
  dgGetAria: (name) => `Get a new key for ${name}`,
  dgLockT: "This page is password-protected",
  dgLockH: "The owner of the subscription set it",
  dgLockWrong: (left) => `That password didn’t match, check the keyboard layout and try again${left > 0 ? `. Tries left: ${left}` : ""}`,
};

const zh: DlgDict = {
  dgMenuAria: "设备操作",
  dgDelete: "删除设备",
  dgDelT: (name) => `删除 ${name}`,
  dgDelD: "这台设备的密钥将失效，名额会空出来。之后只能用新密钥重新添加这台设备",
  dgDelLinkD: "这台设备上的应用将无法再连接，名额会空出来。之后需要在应用中重新添加订阅才能恢复",
  dgRotT: (name) => `更换 ${name} 的密钥`,
  dgRotD: (app) => `旧密钥会立即失效，需要把新密钥重新添加到 ${app}`,
  dgRotBtn: "更换",
  dgNameMax: "最多 40 个字符",
  dgStaleT: (name) => `${name} 需要新密钥`,
  dgStaleD: "服务器已更新，这台设备的旧密钥不能再使用",
  dgStaleDnsD: "服务器的 DNS 已更改，请重新获取密钥。密钥本身不变",
  dgStaleSteps: (app) => ["点击「获取新密钥」", `像第一次那样把它添加到 ${app}`, `在 ${app} 中删除这台设备的旧连接`],
  dgLater: "稍后",
  dgDnsT: "密钥的 DNS",
  dgDnsLead: (app) => `对 ${app} 的密钥生效。订阅类应用的 DNS 目前所有服务器共用一个`,
  dgDnsScope: "适用于哪些服务器",
  dgDnsAll: "所有服务器",
  dgDnsOne: (place) => `仅 ${place}`,
  dgDnsField: "DNS",
  dgDnsHint: "更改后需要重新获取密钥",
  dgLinkLead: (hours) => `应用在更新订阅时会获得新的 DNS（最长 ${hours} 小时）`,
  dgDoneT: "DNS 已更改",
  dgDoneAll: (name, desc) => `现在所有服务器使用 ${name}${desc ? `（${desc}）` : ""}。密钥本身没有变，但每台带密钥的设备都需要重新获取密钥`,
  dgDoneOne: (place, name, desc) => `现在服务器 ${place} 使用 ${name}${desc ? `（${desc}）` : ""}。密钥本身没有变，但每台带密钥的设备都需要重新获取密钥`,
  dgDoneNoneAll: "DNS 已保存，新生成的密钥会使用它",
  dgDoneNoneOne: "DNS 已保存，这台服务器上新生成的密钥会使用它",
  dgDoneLink: (hours) => `订阅类应用在更新订阅时会获得新的 DNS（最长 ${hours} 小时）。想立即生效，请在应用中更新订阅`,
  dgDoneSkip: (names) => `并非所有服务器都有这个选项：${names} 保持不变`,
  dgDoneRow: (country) => `需要重新获取 ${country} 的密钥`,
  dgDoneRowNo: "需要重新获取密钥",
  dgGet: "获取",
  dgGetAria: (name) => `为 ${name} 获取新密钥`,
  dgLockT: "页面已设密码",
  dgLockH: "密码由订阅所有者设置",
  dgLockWrong: (left) => `密码不对，请检查键盘布局后再试${left > 0 ? `。还可以尝试 ${left} 次` : ""}`,
};

const fa: DlgDict = {
  dgMenuAria: "اقدام‌های دستگاه",
  dgDelete: "حذف دستگاه",
  dgDelT: (name) => `حذف ${iso(name)}`,
  dgDelD: "کلید این دستگاه از کار می‌افتد و جایش آزاد می‌شود. دستگاه را فقط با یک کلید تازه می‌توان برگرداند",
  dgDelLinkD: "برنامهٔ این دستگاه دیگر وصل نمی‌شود و جایش آزاد می‌شود. برای برگرداندن دستگاه، اشتراک را دوباره در برنامه اضافه کنید",
  dgRotT: (name) => `تعویض کلید ${iso(name)}`,
  dgRotD: (app) => `کلید قبلی فوراً از کار می‌افتد و کلید جدید را باید دوباره به ${iso(app)} اضافه کنید`,
  dgRotBtn: "تعویض",
  dgNameMax: "حداکثر 40 نویسه",
  dgStaleT: (name) => `کلید جدید برای ${iso(name)} لازم است`,
  dgStaleD: "سرورها به‌روز شده‌اند و کلید قبلی این دستگاه دیگر کار نمی‌کند",
  dgStaleDnsD: "DNS سرور عوض شده — کلید را دوباره بگیرید. خود کلید همان می‌ماند",
  dgStaleSteps: (app) => ["روی «دریافت کلید جدید» بزنید", `مثل بار اول آن را به ${iso(app)} اضافه کنید`, `اتصال قبلی این دستگاه را در ${iso(app)} حذف کنید`],
  dgLater: "بعداً",
  dgDnsT: "DNS برای کلیدها",
  dgDnsLead: (app) => `برای کلیدهای ${iso(app)} اعمال می‌شود. در برنامه‌هایی که با لینک کار می‌کنند فعلاً یک DNS برای همهٔ سرورهاست`,
  dgDnsScope: "برای کدام سرورها",
  dgDnsAll: "همهٔ سرورها",
  dgDnsOne: (place) => `فقط ${iso(place)}`,
  dgDnsField: "DNS",
  dgDnsHint: "پس از تغییر، کلیدها را باید دوباره بگیرید",
  dgLinkLead: (hours) => `برنامه‌ها DNS جدید را موقع به‌روزرسانی اشتراک می‌گیرند (حداکثر تا ${hours} ساعت)`,
  dgDoneT: "DNS تغییر کرد",
  dgDoneAll: (name, desc) => `اکنون روی همهٔ سرورها ${iso(name)}${desc ? ` (${iso(desc)})` : ""} فعال است. خود کلیدها همان ماندند، اما هر دستگاهی که کلید دارد باید آن را دوباره بگیرد`,
  dgDoneOne: (place, name, desc) => `اکنون روی سرور ${iso(place)}، ${iso(name)}${desc ? ` (${iso(desc)})` : ""} فعال است. خود کلید همان ماند، اما هر دستگاهی که کلید دارد باید آن را دوباره بگیرد`,
  dgDoneNoneAll: "DNS ذخیره شد — کلیدهای جدید آن را می‌گیرند",
  dgDoneNoneOne: "DNS ذخیره شد — کلیدهای جدید این سرور آن را می‌گیرند",
  dgDoneLink: (hours) => `برنامه‌هایی که با لینک کار می‌کنند DNS جدید را موقع به‌روزرسانی اشتراک می‌گیرند (حداکثر تا ${hours} ساعت). برای اعمال فوری، اشتراک را در برنامه به‌روز کنید`,
  dgDoneSkip: (names) => `این گزینه روی همهٔ سرورها نیست: ${names} بدون تغییر ماندند`,
  dgDoneRow: (country) => `کلید ${iso(country)} را باید دوباره گرفت`,
  dgDoneRowNo: "کلید را باید دوباره گرفت",
  dgGet: "دریافت",
  dgGetAria: (name) => `دریافت کلید جدید برای ${iso(name)}`,
  dgLockT: "این صفحه با رمز قفل شده است",
  dgLockH: "رمز را مالک اشتراک تعیین کرده است",
  dgLockWrong: (left) => `رمز درست نبود؛ زبان صفحه‌کلید را بررسی کنید و دوباره تلاش کنید${left > 0 ? `. تلاش‌های باقی‌مانده: ${left}` : ""}`,
};

const tr: DlgDict = {
  dgMenuAria: "Cihaz işlemleri",
  dgDelete: "Cihazı sil",
  dgDelT: (name) => `Sil: ${name}`,
  dgDelD: "Bu cihazın anahtarı çalışmayı bırakır ve yeri boşalır. Cihazı yalnızca yeni bir anahtarla geri getirebilirsiniz",
  dgDelLinkD: "Bu cihazdaki uygulama bağlanmayı bırakır ve yeri boşalır. Cihazı geri getirmek için aboneliği uygulamaya yeniden ekleyin",
  dgRotT: (name) => `Anahtarı değiştir: ${name}`,
  dgRotD: (app) => `Eski anahtar hemen çalışmayı bırakır; yenisini ${app} uygulamasına yeniden eklemeniz gerekir`,
  dgRotBtn: "Değiştir",
  dgNameMax: "en çok 40 karakter",
  dgStaleT: (name) => `${name} için yeni anahtar gerekli`,
  dgStaleD: "Sunucular güncellendi ve bu cihazın eski anahtarı artık çalışmıyor",
  dgStaleDnsD: "Sunucunun DNS’i değişti — anahtarı yeniden alın. Anahtarın kendisi aynı kalır",
  dgStaleSteps: (app) => ["“Yeni anahtar al” düğmesine basın", `Anahtarı ${app} uygulamasına ilk seferdeki gibi ekleyin`, `${app} içindeki bu cihazın eski bağlantısını silin`],
  dgLater: "Sonra",
  dgDnsT: "Anahtarlar için DNS",
  dgDnsLead: (app) => `${app} anahtarları için geçerlidir. Bağlantı kullanan uygulamalarda DNS şimdilik tüm sunucular için tektir`,
  dgDnsScope: "Hangi sunucular için",
  dgDnsAll: "Tüm sunucular",
  dgDnsOne: (place) => `Yalnızca ${place}`,
  dgDnsField: "DNS",
  dgDnsHint: "Değişiklikten sonra anahtarlar yeniden alınmalı",
  dgLinkLead: (hours) => `Uygulamalar yeni DNS’i abonelik güncellenirken alır (en çok ${hours} sa)`,
  dgDoneT: "DNS değişti",
  dgDoneAll: (name, desc) => `Artık tüm sunucularda ${name}${desc ? ` (${desc})` : ""} kullanılıyor. Anahtarların kendisi aynı kaldı, ancak anahtarı olan her cihazın onu yeniden alması gerekir`,
  dgDoneOne: (place, name, desc) => `Artık ${place} sunucusunda ${name}${desc ? ` (${desc})` : ""} kullanılıyor. Anahtarın kendisi aynı kaldı, ancak anahtarı olan her cihazın onu yeniden alması gerekir`,
  dgDoneNoneAll: "DNS kaydedildi — yeni anahtarlar onu alır",
  dgDoneNoneOne: "DNS kaydedildi — bu sunucunun yeni anahtarları onu alır",
  dgDoneLink: (hours) => `Bağlantı kullanan uygulamalar yeni DNS’i abonelik güncellenirken alır (en çok ${hours} sa). Hemen almak için uygulamada aboneliği güncelleyin`,
  dgDoneSkip: (names) => `Bu seçenek her sunucuda yok: ${names} olduğu gibi kaldı`,
  dgDoneRow: (country) => `${country} için anahtar yeniden alınmalı`,
  dgDoneRowNo: "Anahtar yeniden alınmalı",
  dgGet: "Al",
  dgGetAria: (name) => `${name} için yeni anahtar al`,
  dgLockT: "Sayfa parolayla kapalı",
  dgLockH: "Parolayı aboneliğin sahibi belirledi",
  dgLockWrong: (left) => `Parola uyuşmadı, klavye düzenini kontrol edip tekrar deneyin${left > 0 ? `. Kalan deneme: ${left}` : ""}`,
};

export const dlg: Record<Lang, DlgDict> = { ru, en, zh, fa, tr };
