package hostctl

// ProvisionUFWTag is the UFW comment the panel's SSH install puts on the 80/tcp, 443/tcp and 443/udp rules it adds to an
// active UFW (internal/panel/provision). Those rules belong to the node: Cleanup removes them when it is retired. The
// SSH rule the install adds carries no tag and is never removed.
const ProvisionUFWTag = "mistgate-node-provision-v1"
