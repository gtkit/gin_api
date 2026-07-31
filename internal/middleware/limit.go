package middleware

import (
	"net/http"
	"strconv"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
	"go.uber.org/zap"

	"github.com/gtkit/logger/v2"

	"github.com/gtkit/golimit"
)

// LimitIp IP限流器.
func LimitIp(num int) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 得到ip地址
		ipAddr := c.ClientIP()
		ipkey := ipAddr + c.Request.Method + c.Request.URL.Path
		// 限制每秒请求次数
		limit := golimit.NewLimiter(ipkey, num)

		// 判断是否超过限制
		if !limit.Allow() {
			logger.Warn("ip_warn ", zap.String("ip 地址: ", ipAddr), zap.Int(" ip 请求太频繁了, 当前限制每秒/ ", num))
			c.AbortWithStatusJSON(http.StatusTooManyRequests,
				resp.NewResponse(int32(goerr.StatusTooManyRequests().Code()), goerr.StatusTooManyRequests().Msg()))
			return
		} else {
			c.Next()
		}
	}
}

// LimitUserID 用户 ID 限流器.
func LimitUserID(num int) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 得到用户ID
		userID := ginc.UserIDFromContext(c)
		userIDkey := strconv.FormatInt(userID, 10) + c.Request.Method + c.Request.URL.Path
		// 限制每秒请求次数
		limit := golimit.NewLimiter(userIDkey, num)

		// 判断是否超过限制
		if !limit.Allow() {
			logger.Warn("user_id_warn ", zap.Int64("用户ID: ", userID), zap.Int(" 用户请求太频繁了, 当前限制每秒/ ", num))
			c.AbortWithStatusJSON(http.StatusTooManyRequests,
				resp.NewResponse(int32(goerr.StatusTooManyRequests().Code()), goerr.StatusTooManyRequests().Msg()))
			return
		} else {
			c.Next()
		}
	}
}
