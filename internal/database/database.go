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
