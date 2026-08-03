package application

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/module/user/dto"
)

// Profile 返回用户资料。
func (s *Service) Profile(ctx context.Context, userID int64) (*dto.UserProfileResponse, error) {
	user, found, err := s.repo.Get(ctx, strconv.FormatInt(userID, 10))
	if err != nil {
		return nil, fmt.Errorf("get user %d: %w", userID, err)
	}
	if !found {
		return nil, fmt.Errorf("profile user %d: %w", userID, ErrUserNotFound)
	}

	return toProfileResponse(user), nil
}

func toProfileResponse(user models.User) *dto.UserProfileResponse {
	return &dto.UserProfileResponse{
		ID:        user.ID,
		Nickname:  user.Nickname,
		Gender:    dto.NormalizeGender(int8(user.Gender)),
		Avatar:    user.Avatar,
		Phone:     maskPhone(user.Phone),
		Status:    int8(user.Status),
		VIPStatus: int8(user.VipStatus),
		VIPLevel:  user.VipLevel,
	}
}

func maskPhone(phone string) string {
	if len(phone) <= 7 {
		return phone
	}
	return phone[:3] + "****" + phone[len(phone)-4:]
}

// 更新用户资料.
// UpdateProfileCommand 是更新用户资料的业务输入（不含 HTTP 请求形状 / binding tag）。
// 仅 Gender 用指针：其零值 0 可能是合法取值，需区分「未传」与「传 0」；
// NickName/Avatar 的空串等同「不更新」，值类型即可表达。
type UpdateProfileCommand struct {
	NickName string
	Gender   *int8
	Avatar   string
}

// UpdateProfile 只更新显式给出的字段（文本非空、Gender 非 nil）；无可更新字段则不触库直接返回。
func (s *Service) UpdateProfile(ctx context.Context, userID int64, cmd UpdateProfileCommand) error {
	fields := make(map[string]any, 3)
	if cmd.NickName != "" {
		// 列名必须与 models.User 的 gorm column 一致（nickname）；
		// Updates(map) 直接把 key 当列名，写错会在真实库上报 Unknown column。
		fields["nickname"] = cmd.NickName
	}
	if cmd.Gender != nil {
		fields["gender"] = models.UserGender(*cmd.Gender)
	}
	if cmd.Avatar != "" {
		fields["avatar"] = cmd.Avatar
	}
	if len(fields) == 0 {
		return nil
	}
	return s.repo.Update(ctx, userID, fields)
}
