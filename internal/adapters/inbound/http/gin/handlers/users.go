package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/responses"
)

type UsersHandler struct {
}

func NewUsersHandler() *UsersHandler {
	return &UsersHandler{}
}

func (h *UsersHandler) GetUsersList(c *gin.Context) {
	responses.OK(c, map[string]string{"GetUsersList": "ok"})
}
