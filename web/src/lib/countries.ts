// Countries offered in "Add node" (ISO 3166-1 alpha-2): where VPN servers usually live. The node's country is the badge
// and the filter, the name friends see for its servers, and it decides the doctor's DNS check: on a node in Russia the
// doctor tests gosuslugi.ru through the server's resolver and offers Yandex DNS. Any other code can still be set through
// the API.
export const countryCodes = [
  "AE", "AM", "AR", "AT", "AU", "AZ", "BE", "BG", "BR", "BY", "CA", "CH", "CL", "CY", "CZ", "DE", "DK", "EE",
  "ES", "FI", "FR", "GB", "GE", "GR", "HK", "HR", "HU", "ID", "IE", "IL", "IN", "IS", "IT", "JP", "KG", "KR",
  "KZ", "LT", "LU", "LV", "MD", "MX", "MY", "NL", "NO", "PL", "PT", "RO", "RS", "RU", "SE", "SG", "SI", "SK",
  "TH", "TR", "TW", "UA", "US", "UZ", "VN", "ZA",
] as const; // prettier-ignore

/** Region indicator flags would draw on some systems and not on Windows, so the UI shows the code itself. */
export const isCountryCode = (v: string) => /^[A-Za-z]{2}$/.test(v);
