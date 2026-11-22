package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/wendaining/cloudlet/migrations"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type Store struct {
	DB       *gorm.DB
	SQL      *sql.DB
	provider *goose.Provider
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"-"`
}

var ErrCredentials = errors.New("邮箱或密码错误")

func Open(ctx context.Context, connection string) (*Store, error) {
	if connection == "" {
		return nil, errors.New("CLOUDLET_DATABASE_URL 未设置")
	}
	db, err := gorm.Open(postgres.Open(connection), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化 PostgreSQL 连接失败: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("连接 PostgreSQL 失败: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files, goose.WithSessionLocker(locker))
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	return &Store{DB: db, SQL: sqlDB, provider: provider}, nil
}

func (s *Store) CheckVersion(ctx context.Context) error {
	version, err := s.provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("读取数据库版本失败: %w", err)
	}
	if version != migrations.Version {
		return fmt.Errorf("数据库版本为 %d，当前代码要求 %d", version, migrations.Version)
	}
	return nil
}

func (s *Store) Migrate(ctx context.Context) error {
	version, err := s.provider.GetDBVersion(ctx)
	if err != nil && !errors.Is(err, goose.ErrNoNextVersion) {
		return err
	}
	if version > migrations.Version {
		return fmt.Errorf("数据库版本 %d 超出当前代码支持的版本 %d", version, migrations.Version)
	}
	if _, err := s.provider.Up(ctx); err != nil {
		return fmt.Errorf("执行数据库迁移失败: %w", err)
	}
	return s.CheckVersion(ctx)
}

func (s *Store) Status(ctx context.Context) ([]*goose.MigrationStatus, error) {
	return s.provider.Status(ctx)
}

func (s *Store) SeedUser(ctx context.Context, email, password string) error {
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(password) < 16 || len(password) > 72 {
		return errors.New("开发账号需要有效邮箱与长度为 16 至 72 字节的密码")
	}
	var existing User
	err = s.DB.WithContext(ctx).Where("email = ?", strings.ToLower(email)).First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	user := User{ID: uuid.NewString(), Email: strings.ToLower(email), PasswordHash: string(hash)}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "email"}}, DoNothing: true}).Create(&user).Error
}

func (s *Store) Authenticate(ctx context.Context, email, password string) (User, error) {
	var user User
	err := s.DB.WithContext(ctx).Where("email = ?", strings.ToLower(email)).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, ErrCredentials
	}
	if err != nil {
		return User{}, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return User{}, ErrCredentials
		}
		return User{}, err
	}
	return user, nil
}

func (s *Store) User(ctx context.Context, id string) (User, error) {
	var user User
	err := s.DB.WithContext(ctx).Where("id = ?", id).First(&user).Error
	return user, err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	var user User
	err := s.DB.WithContext(ctx).Where("email = ?", strings.ToLower(strings.TrimSpace(email))).First(&user).Error
	return user, err
}

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ProjectMember struct {
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
	CreatedAt time.Time
}

type ProjectMemberDetail struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

func (s *Store) CreateProject(ctx context.Context, ownerID, name string) (Project, error) {
	project := Project{ID: uuid.NewString(), Name: name}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&project).Error; err != nil {
			return fmt.Errorf("创建项目 %s: %w", project.ID, err)
		}
		member := ProjectMember{ProjectID: project.ID, UserID: ownerID, Role: "owner"}
		if err := tx.Create(&member).Error; err != nil {
			return fmt.Errorf("创建项目 %s 的所有者关系: %w", project.ID, err)
		}
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return project, nil
}

func (s *Store) Projects(ctx context.Context, userID string) ([]Project, error) {
	var projects []Project
	err := s.DB.WithContext(ctx).
		Select("projects.*").
		Joins("JOIN project_members ON project_members.project_id = projects.id").
		Where("project_members.user_id = ?", userID).
		Order("projects.created_at, projects.id").
		Find(&projects).Error
	return projects, err
}

func (s *Store) Project(ctx context.Context, userID, projectID string) (Project, error) {
	var project Project
	err := s.DB.WithContext(ctx).
		Select("projects.*").
		Joins("JOIN project_members ON project_members.project_id = projects.id").
		Where("projects.id = ? AND project_members.user_id = ?", projectID, userID).
		First(&project).Error
	return project, err
}

func (s *Store) ProjectMember(ctx context.Context, projectID, userID string) (ProjectMember, error) {
	var member ProjectMember
	err := s.DB.WithContext(ctx).Where("project_id = ? AND user_id = ?", projectID, userID).First(&member).Error
	return member, err
}

func (s *Store) ProjectMembers(ctx context.Context, projectID string) ([]ProjectMemberDetail, error) {
	var members []ProjectMemberDetail
	err := s.DB.WithContext(ctx).
		Table("project_members").
		Select("project_members.user_id AS user_id, users.email AS email, project_members.role AS role").
		Joins("JOIN users ON users.id = project_members.user_id").
		Where("project_members.project_id = ?", projectID).
		Order("CASE project_members.role WHEN 'owner' THEN 0 WHEN 'developer' THEN 1 ELSE 2 END, users.email").
		Scan(&members).Error
	return members, err
}

func (s *Store) UpsertProjectMember(ctx context.Context, projectID, userID, role string) error {
	if role != "developer" && role != "viewer" {
		return fmt.Errorf("项目成员角色 %q 无效", role)
	}
	var existing ProjectMember
	err := s.DB.WithContext(ctx).Where("project_id = ? AND user_id = ?", projectID, userID).First(&existing).Error
	if err == nil && existing.Role == "owner" {
		return fmt.Errorf("不能修改项目所有者")
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	member := ProjectMember{ProjectID: projectID, UserID: userID, Role: role}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"role"}),
	}).Create(&member).Error
}
