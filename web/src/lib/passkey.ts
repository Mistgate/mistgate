// WebAuthn JSON <-> browser API glue. The server sends options_json and
// expects credential_json in the browser's native JSON form (see auth.proto).

function parse(optionsJson: string): unknown {
  const parsed = JSON.parse(optionsJson);
  // The contract is the bare options object; tolerate a {"publicKey": ...} wrapper.
  return parsed?.publicKey ?? parsed;
}

function toJson(cred: Credential | null): string {
  if (!(cred instanceof PublicKeyCredential)) throw new DOMException("no credential", "NotAllowedError");
  return JSON.stringify(cred.toJSON());
}

export async function createPasskey(optionsJson: string): Promise<string> {
  const publicKey = PublicKeyCredential.parseCreationOptionsFromJSON(
    parse(optionsJson) as PublicKeyCredentialCreationOptionsJSON,
  );
  return toJson(await navigator.credentials.create({ publicKey }));
}

export async function getPasskey(optionsJson: string): Promise<string> {
  const publicKey = PublicKeyCredential.parseRequestOptionsFromJSON(
    parse(optionsJson) as PublicKeyCredentialRequestOptionsJSON,
  );
  return toJson(await navigator.credentials.get({ publicKey }));
}
