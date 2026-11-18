//go:build !js

package telegram

// The WARP reach alert (internal/panel/warp reach.go) is the VPS edition's: the Cloudflare edition never raises it.

func warpReachTitle(l L) string {
	return l.pick("A site blocks the WARP exit of the node", "Сайт блокирует WARP-выход ноды", "有网站屏蔽了该节点的 WARP 出口")
}

func warpReachReason(l L, p map[string]string) string {
	site, colo := data(p["domain"], 80), data(p["colo"], 16)
	if colo == "" {
		colo = "?"
	}
	return l.pick(site+" showed a block page through the node's WARP in two checks (Cloudflare colo "+colo+"). Restarting WARP may give another exit.",
		site+" в двух проверках показал страницу блокировки через WARP ноды (колокация Cloudflare "+colo+"). Перезапуск WARP может дать другой выход.",
		site+" 在两次检查中通过该节点的 WARP 显示拦截页面（Cloudflare 数据中心 "+colo+"）。重启 WARP 可能换到另一个出口。")
}
