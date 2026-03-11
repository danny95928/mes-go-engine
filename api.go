package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// upgrader 只在这里定义一次
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WebSocket 接入点
// 当有前端连接 ws://localhost:8080/ws 时触发
func WsHandler(c *gin.Context) {
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	// === 关键逻辑 ===
	// 将新连接加入全局池子 (wsClients 定义在 main.go)
	wsMutex.Lock()
	wsClients[ws] = true
	wsMutex.Unlock()

	// 保持连接活跃，直到客户端断开
	// 这里必须有一个循环读取，否则连接会因为 ping/pong 超时而断开
	// 我们不需要在这里写 WriteJSON，因为 main.go 里的 broadcastToClients 会负责发送
	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			break
		}
	}

	// 清理断开的连接
	wsMutex.Lock()
	delete(wsClients, ws)
	wsMutex.Unlock()
	ws.Close()
}

// HTTP API (供 SpringBoot 调用)
func ParamsHandler(c *gin.Context) {
	// currentStatus 定义在 main.go
	c.JSON(200, gin.H{
		"cplex_q_matrix_seed": currentStatus.Prob,
		"device_temp":         currentStatus.Temp,
	})
}
