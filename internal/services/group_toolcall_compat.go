package services

import "gpt-load/internal/models"

func normalizeToolcallCompat(group *models.Group) {
	if group == nil {
		return
	}
	if group.ChannelType != "openai" || group.GroupType == "aggregate" {
		group.ToolcallCompat = false
	}
}

