package rest

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// multipartOverhead is what a multipart body may hold beyond the script and
// files: part headers, boundaries and the name field.
const multipartOverhead = 1 << 20

// RestPostAppBundle godoc
// @ID PostApp
// @Summary Upload an AI application
// @Description Upload the files of an AI application and the script that deploys it, as one multipart/form-data body. Uploading again under a registered name adds the next version (the highest version plus one); earlier versions are kept. A node receiving a version gets every file in one folder and runs the script from that folder; the target node and the addresses of other nodes are given by the deployment request, not here. File names are the filename of each files part, used as is: one name of letters, digits, ".", "_", "+" and "-", not starting with "." (.env is written by the deployment request), at most 255 characters, each name once. A name with a path such as ../x or conf/x is refused, not shortened. The script is at most 64 KiB, at most 256 files are accepted, and the script and files together may not exceed the upload bound of the service (AIAPP_APP_UPLOAD_MAX_BYTES, 256 MiB by default).
// @Tags [App] AI Applications
// @Accept multipart/form-data
// @Produce json
// @Param name formData string true "Application name: lower case letters, digits, underscore and hyphen, starting with a letter or digit, at most 63 characters. No dot: the name becomes part of a docker compose project name on the node" default(open-webui)
// @Param script formData string true "Deployment script. Send it as this text field or as a file part named script (with a filename), exactly once"
// @Param files formData file false "Files the script uses, such as docker-compose.yml. Repeat the part for each file"
// @Success 200 {object} model.AppVersion "The version registered, with the size and SHA-256 of every file"
// @Failure 400 {object} model.SimpleMsg "Not multipart, name or script missing, script given twice, or a file name not allowed"
// @Failure 413 {object} model.SimpleMsg "The script and files exceed the upload bound"
// @Failure 500 {object} model.SimpleMsg "The upload could not be written to the store"
// @Security BearerAuth
// @Router /apps [post]
func (h *Handler) RestPostAppBundle(c echo.Context) error {
	ctx := c.Request().Context()
	limit := h.apps.MaxUploadBytes()
	tooLarge := fmt.Sprintf("the script and files together must be at most %d bytes", limit)

	req := c.Request()
	if req.ContentLength > limit+multipartOverhead {
		return c.JSON(http.StatusRequestEntityTooLarge, model.SimpleMsg{Message: tooLarge})
	}
	req.Body = http.MaxBytesReader(c.Response(), req.Body, limit+multipartOverhead)
	form, err := c.MultipartForm()
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return c.JSON(http.StatusRequestEntityTooLarge, model.SimpleMsg{Message: tooLarge})
		}
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "multipart/form-data body with name, script and files required"})
	}
	defer func() {
		_ = form.RemoveAll()
	}()

	names := form.Value["name"]
	if len(names) != 1 {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "exactly one name field required"})
	}
	name := names[0]
	if len(form.Value["files"]) > 0 {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "files must be sent as file parts with a filename"})
	}

	script, err := formScript(form)
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: err.Error()})
	}

	headers := form.File["files"]
	files := make([]appstore.File, 0, len(headers))
	for _, fh := range headers {
		fileName, err := rawFileName(fh)
		if err != nil {
			return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: err.Error()})
		}
		f, err := fh.Open()
		if err != nil {
			log.Error().Err(err).Str("app", name).Msg("Failed to open uploaded file")
			return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "AI application upload failed"})
		}
		defer func() {
			_ = f.Close()
		}()
		files = append(files, appstore.File{Name: fileName, Content: f})
	}

	registered, err := h.apps.Register(ctx, name, script, files)
	if err != nil {
		log.Warn().Err(err).Str("app", name).Msg("Failed to register AI application")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "AI application upload failed")})
	}
	return c.JSON(http.StatusOK, registered)
}

// formScript reads the script from the script text field or the script file
// part, whichever was sent.
func formScript(form *multipart.Form) (string, error) {
	texts := form.Value["script"]
	parts := form.File["script"]
	switch {
	case len(texts)+len(parts) == 0:
		return "", errors.New("script required, as a text field or a file part")
	case len(texts)+len(parts) > 1:
		return "", errors.New("send the script once, as a text field or a file part")
	case len(texts) == 1:
		return texts[0], nil
	}
	f, err := parts[0].Open()
	if err != nil {
		return "", errors.New("script file part could not be read")
	}
	defer func() {
		_ = f.Close()
	}()
	raw, err := io.ReadAll(io.LimitReader(f, model.MaxAppScriptBytes+1))
	if err != nil {
		return "", errors.New("script file part could not be read")
	}
	return string(raw), nil
}

// rawFileName is the filename a file part was sent with. FileHeader.Filename
// is not used: it is reduced to its last path element, which would turn
// ../x into x instead of refusing it.
func rawFileName(fh *multipart.FileHeader) (string, error) {
	_, params, err := mime.ParseMediaType(fh.Header.Get("Content-Disposition"))
	if err != nil {
		return "", errors.New("files part has a malformed Content-Disposition header")
	}
	return params["filename"], nil
}

// RestGetAllAppBundles godoc
// @ID GetAllApps
// @Summary List AI applications
// @Description List every uploaded AI application, ordered by name, with every version: its number, script, files (name, size and SHA-256) and upload time. Versions are listed oldest first.
// @Tags [App] AI Applications
// @Produce json
// @Success 200 {object} model.AppListResp "AI application list"
// @Security BearerAuth
// @Router /apps [get]
func (h *Handler) RestGetAllAppBundles(c echo.Context) error {
	ctx := c.Request().Context()
	return c.JSON(http.StatusOK, model.AppListResp{Apps: h.apps.List(ctx)})
}

// RestGetAppBundle godoc
// @ID GetApp
// @Summary Get an AI application
// @Description Read one AI application with every version, oldest first
// @Tags [App] AI Applications
// @Produce json
// @Param name path string true "Application name" default(open-webui)
// @Success 200 {object} model.App "AI application"
// @Failure 404 {object} model.SimpleMsg "AI application not found"
// @Security BearerAuth
// @Router /apps/{name} [get]
func (h *Handler) RestGetAppBundle(c echo.Context) error {
	ctx := c.Request().Context()
	name := c.Param("name")

	app, err := h.apps.Get(ctx, name)
	if err != nil {
		log.Warn().Err(err).Str("app", name).Msg("Failed to get AI application")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "AI application not found")})
	}
	return c.JSON(http.StatusOK, app)
}

// RestGetAppBundleVersion godoc
// @ID GetAppVersion
// @Summary Get one version of an AI application
// @Description Read one version of an AI application: its script and the name, size and SHA-256 of every file
// @Tags [App] AI Applications
// @Produce json
// @Param name path string true "Application name" default(open-webui)
// @Param version path int true "Version number, from 1" default(1)
// @Success 200 {object} model.AppVersion "AI application version"
// @Failure 400 {object} model.SimpleMsg "version is not a positive integer"
// @Failure 404 {object} model.SimpleMsg "AI application or version not found"
// @Security BearerAuth
// @Router /apps/{name}/versions/{version} [get]
func (h *Handler) RestGetAppBundleVersion(c echo.Context) error {
	ctx := c.Request().Context()
	name := c.Param("name")
	version, err := strconv.Atoi(c.Param("version"))
	if err != nil || version < 1 {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "version must be a positive integer"})
	}

	v, err := h.apps.GetVersion(ctx, name, version)
	if err != nil {
		log.Warn().Err(err).Str("app", name).Int("version", version).Msg("Failed to get AI application version")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "AI application version not found")})
	}
	return c.JSON(http.StatusOK, v)
}

// RestDeleteAppBundle godoc
// @ID DeleteApp
// @Summary Delete an AI application
// @Description Remove an AI application with every version and its stored files. Applications already deployed on nodes are untouched
// @Tags [App] AI Applications
// @Produce json
// @Param name path string true "Application name"
// @Success 200 {object} model.SimpleMsg "AI application deleted"
// @Failure 404 {object} model.SimpleMsg "AI application not found"
// @Failure 500 {object} model.SimpleMsg "The stored files could not be removed"
// @Security BearerAuth
// @Router /apps/{name} [delete]
func (h *Handler) RestDeleteAppBundle(c echo.Context) error {
	ctx := c.Request().Context()
	name := c.Param("name")

	if err := h.apps.Delete(ctx, name); err != nil {
		log.Warn().Err(err).Str("app", name).Msg("Failed to delete AI application")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "AI application deletion failed")})
	}
	return c.JSON(http.StatusOK, model.SimpleMsg{Message: "AI application deleted"})
}
