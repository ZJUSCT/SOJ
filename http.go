package main

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

// listSubmitsHandler
// list submits with limited information
// does not need to be authenticated
func listSubmitsHandler(c *gin.Context) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page <= 0 {
		c.JSON(400, gin.H{
			"message": "Invalid parameter: page",
		})
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil {
		c.JSON(400, gin.H{
			"message": "Invalid parameter: limit",
		})
		return
	}

	var submits []SubmitCtx
	var total int64
	db.Select("id", "user", "problem", "submit_time", "last_update", "status", "msg", "judge_result").
		Order("submit_time desc").
		Offset((page - 1) * limit).Limit(limit).
		Find(&submits)
	db.Model(&SubmitCtx{}).Count(&total)

	c.JSON(200, gin.H{
		"code":    0,
		"message": "success",
		"data": gin.H{
			"total":   total,
			"submits": submits,
		},
	})
}

// listRankHandler
// list rank of users
func listRankHandler(c *gin.Context) {
	var users []User
	db.Order("total_score desc").Find(&users)

	c.JSON(200, gin.H{
		"code":    0,
		"message": "success",
		"data":    users,
	})
}

func serveHTTP(addr string) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()
	err := router.SetTrustedProxies([]string{"127.0.0.1"})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to set trusted proxies")
		return
	}

	router.GET("/api/v1/submits/list", listSubmitsHandler)
	router.GET("/api/v1/rank/list", listRankHandler)

	go func() {
		log.Info().Str("addr", addr).Msg("HTTP server started")
		err = router.Run(addr)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to start HTTP server")
		}
	}()
}
