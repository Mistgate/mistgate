// A random secret for a protocol field (an obfuscation password): 32 URL-safe characters from 24 random bytes,
// the same shape the panel generates for a new profile. Made in the browser so the admin can see and copy it
// before saving; the panel stores it encrypted and never sends it back (it returns "••••").

export function generateSecret(bytes = 24): string {
  const b = crypto.getRandomValues(new Uint8Array(bytes));
  let bin = "";
  for (const x of b) bin += String.fromCharCode(x);
  return btoa(bin).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}
