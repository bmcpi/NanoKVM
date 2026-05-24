package redfish

import (
	"fmt"
	"net/http"
	"time"

	"github.com/BMCPi/NanoKVM/server/middleware"
	"github.com/BMCPi/NanoKVM/server/service/auth"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Service) GetSessionService(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"@odata.type":    "#SessionService.v1_1_8.SessionService",
		"@odata.id":      "/redfish/v1/SessionService",
		"@odata.context": "/redfish/v1/$metadata#SessionService.SessionService",
		"Id":             "SessionService",
		"Name":           "Session Service",
		"Sessions": gin.H{
			"@odata.id": "/redfish/v1/SessionService/Sessions",
		},
	})
}

func (s *Service) CreateSession(c *gin.Context) {
	var req struct {
		UserName string `json:"UserName"`
		Password string `json:"Password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		redfishErrorResponse(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if ok := auth.ComparePlainAccount(req.UserName, req.Password); !ok {
		time.Sleep(2 * time.Second)
		redfishErrorResponse(c, http.StatusUnauthorized, "invalid username or password")
		return
	}

	token, err := middleware.GenerateJWT(req.UserName)
	if err != nil {
		redfishErrorResponse(c, http.StatusInternalServerError, "failed to generate token")
		return
	}

	sessionID := fmt.Sprintf("%d", time.Now().UnixNano())

	log.Debugf("redfish session created for user: %s", req.UserName)

	c.Header("X-Auth-Token", token)
	c.JSON(http.StatusCreated, gin.H{
		"@odata.type":    "#Session.v1_3_0.Session",
		"@odata.id":      "/redfish/v1/SessionService/Sessions/" + sessionID,
		"@odata.context": "/redfish/v1/$metadata#Session.Session",
		"Id":             sessionID,
		"Name":           "User Session",
		"UserName":       req.UserName,
	})
}

func (s *Service) GetSessionCollection(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"@odata.type":         "#SessionCollection.SessionCollection",
		"@odata.id":           "/redfish/v1/SessionService/Sessions",
		"@odata.context":      "/redfish/v1/$metadata#SessionCollection.SessionCollection",
		"Name":                "Session Collection",
		"Members@odata.count": 0,
		"Members":             []gin.H{},
	})
}

func (s *Service) DeleteSession(c *gin.Context) {
	// Stub — session management is not persisted
	c.Status(http.StatusNoContent)
}
