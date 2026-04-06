package model

import (
	"context"
	"time"

	"github.com/openimsdk/open-im-server/v3/tools/db/mongoutil"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type OrganizationUserRole string

const (
	OrganizationUserSuperAdminRole   OrganizationUserRole = "SuperAdmin"
	OrganizationUserBackendAdminRole OrganizationUserRole = "BackendAdmin"
	OrganizationUserGroupManagerRole OrganizationUserRole = "GroupManager"
	OrganizationUserTermManagerRole  OrganizationUserRole = "TermManager"
	OrganizationUserNormalRole       OrganizationUserRole = "Normal"
)

type PermissionCode string

const (
	PermissionCodeModifyNickname   PermissionCode = "modify_nickname"    // 允许修改昵称
	PermissionCodeSendFile         PermissionCode = "send_file"          // 允许发送文件
	PermissionCodeSendBusinessCard PermissionCode = "send_business_card" // 允许发送名片
	PermissionCodeCreateGroup      PermissionCode = "create_group"       // 允许建群
	PermissionCodeAddFriend        PermissionCode = "add_friend"         // 允许加好友
)

type OrganizationRolePermission struct {
	ID primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`

	OrgId primitive.ObjectID   `bson:"org_id" json:"org_id"`
	Role  OrganizationUserRole `bson:"role" json:"role"`

	PermissionCode PermissionCode `bson:"permission_code" json:"permission_code"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

func (OrganizationRolePermission) TableName() string {
	return "organization_role_permission"
}

type OrganizationRolePermissionDao struct {
	DB         *mongo.Database
	Collection *mongo.Collection
}

func NewOrganizationRolePermissionDao(db *mongo.Database) *OrganizationRolePermissionDao {
	return &OrganizationRolePermissionDao{
		DB:         db,
		Collection: db.Collection(OrganizationRolePermission{}.TableName()),
	}
}

func (o *OrganizationRolePermissionDao) Create(ctx context.Context, obj *OrganizationRolePermission) error {
	obj.UpdatedAt = time.Now().UTC()
	obj.CreatedAt = time.Now().UTC()
	return mongoutil.InsertMany(ctx, o.Collection, []*OrganizationRolePermission{obj})
}

func (o *OrganizationRolePermissionDao) GetByOrgIdAndRole(ctx context.Context, orgId primitive.ObjectID, role OrganizationUserRole) ([]*OrganizationRolePermission, error) {
	return mongoutil.Find[*OrganizationRolePermission](ctx, o.Collection, bson.M{"org_id": orgId, "role": role})
}

func (o *OrganizationRolePermissionDao) DeleteByOrgIdAndRole(ctx context.Context, orgId primitive.ObjectID, role OrganizationUserRole) error {
	filter := bson.M{}
	filter["org_id"] = orgId
	filter["role"] = role
	return mongoutil.DeleteMany(ctx, o.Collection, filter)
}

func (o *OrganizationRolePermissionDao) ExistPermission(ctx context.Context, orgId primitive.ObjectID, role OrganizationUserRole, code PermissionCode) (bool, error) {
	return mongoutil.Exist(ctx, o.Collection, bson.M{"org_id": orgId, "role": role, "permission_code": code})
}
