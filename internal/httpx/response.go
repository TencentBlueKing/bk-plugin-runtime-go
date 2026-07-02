package httpx

import (
	"net/http"

	"github.com/TencentBlueKing/bk-plugin-framework-go/protocol"
	"github.com/gin-gonic/gin"
)

type Envelope = protocol.Response

func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, protocol.OK(data))
}

func OKWithTrace(c *gin.Context, traceID string, data interface{}) {
	c.JSON(http.StatusOK, gin.H{
		"result":   true,
		"code":     0,
		"message":  "success",
		"data":     data,
		"trace_id": traceID,
	})
}

func Error(c *gin.Context, status int, code int, message string) {
	c.JSON(status, protocol.Error(code, message))
}
