package tenantmemberships

import "time"

type TenantMembershipsStatus string

const (
	TenantMembershipsStatusInvited   TenantMembershipsStatus = "invited"   // 已邀请，尚未正式加入
	TenantMembershipsStatusActive    TenantMembershipsStatus = "active"    // 正常成员
	TenantMembershipsStatusSuspended TenantMembershipsStatus = "suspended" // 成员被暂停
	TenantMembershipsStatusLeft      TenantMembershipsStatus = "left"      // 已离开租户
)

type TenantMemberships struct {
	ID          string                  //	成员关系主键 UUIDv7
	TenantID    string                  //	所属租户  UUIDv7
	UserID      string                  //	对应的用户  UUIDv7
	Status      TenantMembershipsStatus //	成员关系状态
	Invited_by  string                  //	邀请人的用户 ID
	InvitedAt   time.Time               //	邀请时间
	JoinedAt    time.Time               //	正式加入时间
	SuspendedAt time.Time               //	成员被暂停的时间
	LeftAt      time.Time               //	成员离开时间
	CreatedAt   time.Time               //	关系创建时间
	UpdatedAt   time.Time               //	最后更新时间
}
