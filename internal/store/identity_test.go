package store

import "testing"

func TestIdentityMetadataMigrationPreservesLegacyDevices(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	legacy := Device{ID: "legacy-device", Name: "旧设备"}
	if err = s.Put("devices", legacy.ID, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal("未升级身份元数据版本", version, err)
	}
	var devices []Device
	if err = s.All("devices", &devices); err != nil || len(devices) != 1 || devices[0].ID != legacy.ID || devices[0].PublicKey != "" || devices[0].Revoked {
		t.Fatal("旧身份被丢失或自动绑定", devices, err)
	}
}
