// Countries offered in "Add node" (ISO 3166-1 alpha-2): where VPN servers usually live. The node's country is the badge
// and the filter, the name friends see for its servers, and it decides the doctor's DNS check: on a node in Russia the
// doctor tests gosuslugi.ru through the server's resolver and offers Yandex DNS. Any other code can still be set through
// the API.
export const countryCodes = [
  "AE", "AM", "AT", "AU", "BG", "BR", "CA", "CH", "CZ", "DE", "DK", "EE", "ES", "FI", "FR", "GB", "GE", "HK",
  "HU", "IE", "IL", "IN", "IT", "JP", "KR", "KZ", "LT", "LU", "LV", "MD", "NL", "NO", "PL", "RO", "RS", "RU",
  "SE", "SG", "TR", "UA", "US",
] as const; // prettier-ignore

/** Region indicator flags would draw on some systems and not on Windows, so the UI shows the code itself. */
export const isCountryCode = (v: string) => /^[A-Za-z]{2}$/.test(v);
