package database

import "testing"

// TestSplitMigrationStatements 确保多语句 SQL 不依赖 MySQL multiStatements DSN。
func TestSplitMigrationStatements(t *testing.T) {
	// 只校验分句和事务包装过滤；引号中的分号不能被切成另一条 SQL。
	sqlText := `
-- keep comment
SET NAMES utf8mb4;
CREATE TABLE demo (
  name varchar(32) COMMENT 'a;b',
  remark varchar(32) COMMENT "c;d"
);
BEGIN;
INSERT INTO demo(name) VALUES ('x;y');
COMMIT;
`
	statements := splitMigrationStatements(sqlText)
	if len(statements) != 5 {
		t.Fatalf("splitMigrationStatements() len = %d, want 5: %#v", len(statements), statements)
	}
	if shouldSkipMigrationStatement(statements[2]) == false || shouldSkipMigrationStatement(statements[4]) == false {
		t.Fatalf("BEGIN/COMMIT should be skipped: %#v", statements)
	}
	if shouldSkipMigrationStatement(statements[1]) {
		t.Fatalf("CREATE TABLE should not be skipped: %q", statements[1])
	}
}
