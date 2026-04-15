package picoclaw

import (
	"github.com/gin-gonic/gin"
)

// Screenshot returns an error because HDMI capture is not available in serial-only mode.
func (s *Service) Screenshot(c *gin.Context) {
	writePicoclawError(c, newPicoclawError(CodeScreenshotFailed, "HDMI capture not available in serial-only mode"))
}
