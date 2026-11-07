-- What a node's machine is short of, per hour, from the HostMetrics fields agents send since "resources" (agent.proto 11 to 20):
-- CPU steal and I/O wait, swap, connection tracking, sockets, the NIC's link speed, and the agent process's own CPU and memory.
-- ext_samples counts the samples that carried them, so an hour in which the agent was updated averages over the right count;
-- sessions_sum gives the mean people online of the hour, which the capacity read sets against the CPU and the traffic.

-- +goose Up
ALTER TABLE node_host_hour ADD COLUMN ext_samples        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN sessions_sum       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN steal_sum_x100     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN steal_max_x100     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN iowait_max_x100    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN swap_used_max      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN conntrack_max      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN conntrack_limit    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN tcp_sockets_max    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN nic_speed_mbps     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN agent_cpu_sum_x100 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN agent_cpu_max_x100 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_host_hour ADD COLUMN agent_rss_max      INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE node_host_hour DROP COLUMN agent_rss_max;
ALTER TABLE node_host_hour DROP COLUMN agent_cpu_max_x100;
ALTER TABLE node_host_hour DROP COLUMN agent_cpu_sum_x100;
ALTER TABLE node_host_hour DROP COLUMN nic_speed_mbps;
ALTER TABLE node_host_hour DROP COLUMN tcp_sockets_max;
ALTER TABLE node_host_hour DROP COLUMN conntrack_limit;
ALTER TABLE node_host_hour DROP COLUMN conntrack_max;
ALTER TABLE node_host_hour DROP COLUMN swap_used_max;
ALTER TABLE node_host_hour DROP COLUMN iowait_max_x100;
ALTER TABLE node_host_hour DROP COLUMN steal_max_x100;
ALTER TABLE node_host_hour DROP COLUMN steal_sum_x100;
ALTER TABLE node_host_hour DROP COLUMN sessions_sum;
ALTER TABLE node_host_hour DROP COLUMN ext_samples;
