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

func Error(c *gin.Context, status int, code int, message string) {
	c.JSON(status, protocol.Error(code, message))
}
