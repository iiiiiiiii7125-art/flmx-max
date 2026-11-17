package repo

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// C-(6)③（流量审计）：用户配额限额从整数 GB 改为小数 GB
// （0.01、1.5 均合法）。
//
// SQLite：列类型亲和灵活，整数值存 REAL 无损，AutoMigrate 不改
// 列类型也无影响——无需改列，直接记版本。
//
// PostgreSQL：user_quota.daily_limit_gb / monthly_limit_gb 原为
// bigint，存小数会被截断/报错；本迁移把两列改为 double precision
// （存量整数值照常读为小数，无损）。幂等：已记版本即跳过。
const migrationQuotaDecimalV1Version = "quota_decimal_v1"

// migrateV1QuotaDecimal 执行 C-(6)③ 存量迁移（幂等）。
func migrateV1QuotaDecimal(db *gorm.DB) error {
	if db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := db.Where("version = ?", migrationQuotaDecimalV1Version).First(&m).Error; err == nil {
			log.Printf("migration %s already applied, skipping", migrationQuotaDecimalV1Version)
			return nil
		}
	} else {
		if err := db.Migrator().CreateTable(&schemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migration: %w", err)
		}
	}

	if db.Dialector.Name() == "postgres" {
		if !db.Migrator().HasTable("user_quota") {
			// 新装库：无存量表可迁，直接记版本（与 v6 同口径）。
			log.Printf("migration %s: no user_quota table, nothing to migrate", migrationQuotaDecimalV1Version)
		} else {
			for _, col := range []string{"daily_limit_gb", "monthly_limit_gb"} {
				if err := db.Exec(fmt.Sprintf("ALTER TABLE user_quota ALTER COLUMN %s TYPE double precision", col)).Error; err != nil {
					return fmt.Errorf("migration %s: alter user_quota.%s: %w", migrationQuotaDecimalV1Version, col, err)
				}
			}
			log.Printf("migration %s: user_quota daily/monthly_limit_gb now double precision", migrationQuotaDecimalV1Version)
		}
	} else {
		log.Printf("migration %s: sqlite needs no column change, recording version", migrationQuotaDecimalV1Version)
	}

	if err := db.Create(&schemaMigration{
		Version:   migrationQuotaDecimalV1Version,
		AppliedAt: time.Now().Unix(),
	}).Error; err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}
	log.Printf("migration %s applied", migrationQuotaDecimalV1Version)
	return nil
}
