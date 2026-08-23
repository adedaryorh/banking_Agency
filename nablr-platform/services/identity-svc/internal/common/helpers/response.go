package helpers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

type AppError struct {
	sourceError error
	Status      uint16
}

type ResponseObject struct {
	Code    int         `json:"code,omitempty"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

func Success(c *gin.Context, code int, message string, data interface{}) {
	obj := ResponseObject{
		Code:    code,
		Message: message,
	}
	if data != nil {
		obj.Data = data
	}

	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(code)

	enc := json.NewEncoder(c.Writer)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(obj)
}

func Failure(c *gin.Context, code int, message string, error interface{}) {
	obj := ResponseObject{
		Code:    code,
		Message: message,
		Error:   error,
	}
	c.JSON(code, obj)
}

func AppErrorFailure(c *gin.Context, appError AppError) {
	message := appError.Error()
	if appError.Status == http.StatusInternalServerError || appError.Status == http.StatusFailedDependency {
		log.Error().Msgf("APP INTERNAL SERVER ERROR: status=%v error=%v", appError.Status, appError.Error())
		if appError.Status == http.StatusInternalServerError {
			message = "Request processing failed"
		}
	}
	obj := ResponseObject{
		Code:    int(appError.Status),
		Message: message,
		Error:   message,
	}
	c.JSON(int(appError.Status), obj)
}

func (a *AppError) Error() string {
	return a.sourceError.Error()
}

func NewAppErrorFromStr(message string, status uint16) *AppError {
	err := AppError{sourceError: errors.New(message), Status: status}
	return &err
}

func NewAppErrorFromError(srcError error, status uint16) *AppError {
	err := AppError{sourceError: srcError, Status: status}
	return &err
}

func NewAppInternalError(srcError error) *AppError {
	err := AppError{sourceError: srcError, Status: http.StatusInternalServerError}
	return &err
}
