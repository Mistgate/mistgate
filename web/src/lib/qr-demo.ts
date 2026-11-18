import { create } from "@bufbuild/protobuf";
import { QrCodeSchema } from "@/gen/mistgate/admin/v1/common_pb";

// A made-up code for the scenes of the dev kit and the tests (a 41-module code with a hole for the logo, as EncodeQr returns one
// for a link of about 40 characters). It encodes nothing anyone should scan.
const bits =
  "/s4oHz/BIHyNEG6wvQ6rt1eSC+XboUChIuwXP1vpB/qqqqr+Aa5hLABXufbw9pQFgiVzVMTo2keHIsd/BWS2mUbeRmgUeaKyH8wFcKrTlKLdUFoveD2VQFIYgBnXyOcwkjqPXJviHvoT8ud8h17iNT9qVTGkDZOdErQG/5zAzfCAPoRAqjfknOO+Lq38rdscvbWLzBOrkUaDtqoUnhBSKfM1OZurhrpC+4BbojHE/6Y0mSpQUuyakeuizcKP1dafAydO6SvmiDcFWlTLov4xcgf9AA==";

export const demoQr = create(QrCodeSchema, { n: 41, bits: Uint8Array.from(atob(bits), (c) => c.charCodeAt(0)), window: 9.98 });
