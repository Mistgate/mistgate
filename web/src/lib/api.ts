import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AuthService } from "@/gen/mistgate/admin/v1/auth_pb";
import { AwgService } from "@/gen/mistgate/admin/v1/awg_pb";
import { DeviceService } from "@/gen/mistgate/admin/v1/device_pb";
import { DnsService } from "@/gen/mistgate/admin/v1/dns_pb";
import { FleetService } from "@/gen/mistgate/admin/v1/fleet_pb";
import { GroupService } from "@/gen/mistgate/admin/v1/group_pb";
import { HealthService } from "@/gen/mistgate/admin/v1/health_pb";
import { InstanceService } from "@/gen/mistgate/admin/v1/instance_pb";
import { ApiTokenService, ApprovalService } from "@/gen/mistgate/admin/v1/integrations_pb";
import { NodeService } from "@/gen/mistgate/admin/v1/node_pb";
import { ProfileService } from "@/gen/mistgate/admin/v1/profile_pb";
import { SubscriptionService } from "@/gen/mistgate/admin/v1/subscription_pb";
import { UpdateService } from "@/gen/mistgate/admin/v1/update_pb";
import { UserService } from "@/gen/mistgate/admin/v1/user_pb";
import { WarpService } from "@/gen/mistgate/admin/v1/warp_pb";
import type { MessageKey } from "@/i18n/en";

// The panel rewrites <base href> to the admin prefix (e.g. "/4hbx.../");
// everything below derives from document.baseURI, nothing is hard-coded.
export const basepath = new URL(document.baseURI).pathname;

const transport = createConnectTransport({
  baseUrl: new URL("api", document.baseURI).href,
  useBinaryFormat: false,
});

export const auth = createClient(AuthService, transport);
export const instance = createClient(InstanceService, transport);
export const fleet = createClient(FleetService, transport);
export const nodes = createClient(NodeService, transport);
export const profiles = createClient(ProfileService, transport);
export const users = createClient(UserService, transport);
export const groups = createClient(GroupService, transport);
export const subscriptions = createClient(SubscriptionService, transport);
export const dns = createClient(DnsService, transport);
export const health = createClient(HealthService, transport);
export const updates = createClient(UpdateService, transport);
export const devices = createClient(DeviceService, transport);
export const awg = createClient(AwgService, transport);
export const warp = createClient(WarpService, transport);
export const apiTokens = createClient(ApiTokenService, transport);
export const approvals = createClient(ApprovalService, transport);

/** URL of a file next to the admin UI (respects the secret prefix), e.g. assetUrl("brand/logo.svg"). */
export const assetUrl = (path: string) => new URL(path, document.baseURI).href;

export function isUnauthenticated(e: unknown): boolean {
  return ConnectError.from(e).code === Code.Unauthenticated;
}

export type AuthContext = "setup" | "login";

export function webauthnSupported(): boolean {
  return (
    typeof window.PublicKeyCredential === "function" &&
    typeof PublicKeyCredential.parseCreationOptionsFromJSON === "function" &&
    typeof PublicKeyCredential.parseRequestOptionsFromJSON === "function"
  );
}

/** Turnstile refused a sign-in or setup call: no token, a used or expired one, or Cloudflare unreachable. */
export function isCaptchaError(e: unknown): boolean {
  const c = ConnectError.from(e);
  return c.code === Code.PermissionDenied && c.rawMessage.startsWith("captcha");
}

/** Maps a failure from a WebAuthn ceremony or an auth RPC to a message key. */
export function errorKey(e: unknown, ctx: AuthContext): MessageKey {
  if (e instanceof DOMException) {
    switch (e.name) {
      case "NotAllowedError":
      case "AbortError":
        return "err.cancelled";
      case "InvalidStateError":
        return "err.alreadyRegistered";
      case "SecurityError":
        return "err.origin";
      default:
        return "err.generic";
    }
  }
  const c = ConnectError.from(e);
  if (isCaptchaError(c)) return "err.captcha";
  switch (c.code) {
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return "err.network";
    case Code.ResourceExhausted:
      return "err.rateLimited";
    case Code.Unauthenticated:
    case Code.PermissionDenied:
    case Code.NotFound:
      return ctx === "setup" ? "err.setupLink" : "err.notRecognised";
    default:
      return "err.generic";
  }
}
