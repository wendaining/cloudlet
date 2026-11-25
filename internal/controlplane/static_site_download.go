package controlplane

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
	"gorm.io/gorm"
)

func (h *handler) DownloadStaticSiteArtifact(c *gin.Context, projectID, siteName, versionID string) {
	_, projectID, _, ok := h.projectAccess(c, projectID)
	if !ok || !h.siteStorageAvailable(c) {
		return
	}
	id, err := uuid.Parse(versionID)
	if err != nil {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "站点版本不存在"})
		return
	}
	archive, digest, err := h.sites.OpenArchive(c.Request.Context(), projectID, siteName, id)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, staticdeploy.ErrInvalidSiteName):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "站点版本不存在"})
		return
	case err != nil:
		h.failure(c, err)
		return
	}
	defer func() {
		if err := archive.Close(); err != nil {
			h.logger.ErrorContext(c.Request.Context(), "close downloaded site ZIP", "version", id, "error", err)
		}
	}()
	info, err := archive.Stat()
	if err != nil {
		h.failure(c, fmt.Errorf("inspect ZIP for version %s: %w", id, err))
		return
	}
	checksum, err := hex.DecodeString(digest)
	if err != nil {
		h.failure(c, fmt.Errorf("decode ZIP digest for version %s: %w", id, err))
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": siteName + "-" + id.String() + ".zip"}))
	c.Header("Content-Length", strconv.FormatInt(info.Size(), 10))
	c.Header("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(checksum)+":")
	c.Header("Cache-Control", "private, no-store")
	c.Status(http.StatusOK)
	if _, err := io.CopyN(c.Writer, archive, info.Size()); err != nil {
		h.logger.ErrorContext(c.Request.Context(), "stream downloaded site ZIP", "version", id, "error", err)
	}
}
