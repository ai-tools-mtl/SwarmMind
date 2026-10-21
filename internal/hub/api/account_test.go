package api

// 管理员账户单测（2026-10-21 验收反馈：密码登录体系）。

import (
	"testing"
	"time"
)

func TestAccountLifecycle(t *testing.T) {
	dir := t.TempDir()
	as := newAccountStore(dir)
	now := time.Now()

	if as.initialized() {
		t.Fatal("新 store 不应已初始化")
	}

	// setup
	if err := as.setup("admin", "password123", now); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !as.initialized() {
		t.Fatal("setup 后应已初始化")
	}
	// 弱密码/非法用户名
	if err := as.setup("x", "password123", now); err == nil {
		t.Fatal("已初始化后重复 setup 应拒绝")
	}

	// 登录：正确/错误
	if !as.loginPassword("admin", "password123") {
		t.Fatal("正确密码应通过")
	}
	if as.loginPassword("admin", "wrong-pass") {
		t.Fatal("错误密码应拒绝")
	}
	if as.loginPassword("Admin", "password123") {
		t.Fatal("用户名大小写敏感")
	}

	// 改用户名（需密码）
	if err := as.updateUsername("password123", "lun", now.Add(time.Minute)); err != nil {
		t.Fatalf("updateUsername: %v", err)
	}
	if as.loginPassword("admin", "password123") {
		t.Fatal("旧用户名应失效")
	}
	if !as.loginPassword("lun", "password123") {
		t.Fatal("新用户名应可登录")
	}
	// 错误密码改用户名应拒绝
	if err := as.updateUsername("wrong", "lun2", now); err == nil {
		t.Fatal("密码错误不应允许改用户名")
	}

	// 改密码
	if err := as.changePassword("password123", "newpass456", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("changePassword: %v", err)
	}
	if as.loginPassword("lun", "password123") {
		t.Fatal("旧密码应失效")
	}
	if !as.loginPassword("lun", "newpass456") {
		t.Fatal("新密码应可登录")
	}
	// 相同密码拒绝
	if err := as.changePassword("newpass456", "newpass456", now); err == nil {
		t.Fatal("新旧密码相同应拒绝")
	}

	// root 重置（恢复通道）
	if err := as.resetPasswordRoot("admin", "resetpass789", now.Add(3*time.Minute)); err != nil {
		t.Fatalf("resetPasswordRoot: %v", err)
	}
	if !as.loginPassword("admin", "resetpass789") {
		t.Fatal("root 重置后新凭据应可登录")
	}

	// 持久化：同目录新实例（模拟重启）后仍可登录
	as2 := newAccountStore(dir)
	if !as2.loginPassword("admin", "resetpass789") {
		t.Fatal("重启后账户应保留")
	}
}

// PBKDF2 RFC 6070 风格自检（SHA-256 参考向量）。
func TestPBKDF2Vector(t *testing.T) {
	// PBKDF2-HMAC-SHA256("password", "salt", 1, 32) 已知值前 8 字节 120fb6cffcf8b32c
	got := pbkdf2([]byte("password"), []byte("salt"), 1, 32)
	wantPrefix := []byte{0x12, 0x0f, 0xb6, 0xcf, 0xfc, 0xf8, 0xb3, 0x2c}
	for i, w := range wantPrefix {
		if got[i] != w {
			t.Fatalf("PBKDF2 向量不匹配 byte[%d]=%02x want %02x", i, got[i], w)
		}
	}
	// 迭代次数变化 → 输出变化
	if string(pbkdf2([]byte("password"), []byte("salt"), 2, 32)) == string(got) {
		t.Fatal("不同迭代次数不应产生相同输出")
	}
}
