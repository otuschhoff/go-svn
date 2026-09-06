package wc

const schema31 = `
PRAGMA user_version = 31;
CREATE TABLE REPOSITORY (id INTEGER PRIMARY KEY AUTOINCREMENT, root TEXT UNIQUE NOT NULL, uuid TEXT NOT NULL);
CREATE INDEX I_UUID ON REPOSITORY (uuid);
CREATE INDEX I_ROOT ON REPOSITORY (root);
CREATE TABLE WCROOT (id INTEGER PRIMARY KEY AUTOINCREMENT, local_abspath TEXT UNIQUE);
CREATE UNIQUE INDEX I_LOCAL_ABSPATH ON WCROOT (local_abspath);
CREATE TABLE PRISTINE (checksum TEXT NOT NULL PRIMARY KEY, compression INTEGER, size INTEGER NOT NULL, refcount INTEGER NOT NULL, md5_checksum TEXT NOT NULL);
CREATE INDEX I_PRISTINE_MD5 ON PRISTINE (md5_checksum);
CREATE TABLE ACTUAL_NODE (
  wc_id INTEGER NOT NULL REFERENCES WCROOT (id), local_relpath TEXT NOT NULL, parent_relpath TEXT,
  properties BLOB, conflict_old TEXT, conflict_new TEXT, conflict_working TEXT, prop_reject TEXT,
  changelist TEXT, text_mod TEXT, tree_conflict_data TEXT, conflict_data BLOB,
  older_checksum TEXT REFERENCES PRISTINE (checksum), left_checksum TEXT REFERENCES PRISTINE (checksum),
  right_checksum TEXT REFERENCES PRISTINE (checksum), PRIMARY KEY (wc_id, local_relpath));
CREATE UNIQUE INDEX I_ACTUAL_PARENT ON ACTUAL_NODE (wc_id, parent_relpath, local_relpath);
CREATE TABLE LOCK (repos_id INTEGER NOT NULL REFERENCES REPOSITORY (id), repos_relpath TEXT NOT NULL,
  lock_token TEXT NOT NULL, lock_owner TEXT, lock_comment TEXT, lock_date INTEGER,
  PRIMARY KEY (repos_id, repos_relpath));
CREATE TABLE WORK_QUEUE (id INTEGER PRIMARY KEY AUTOINCREMENT, work BLOB NOT NULL);
CREATE TABLE WC_LOCK (wc_id INTEGER NOT NULL REFERENCES WCROOT (id), local_dir_relpath TEXT NOT NULL,
  locked_levels INTEGER NOT NULL DEFAULT -1, PRIMARY KEY (wc_id, local_dir_relpath));
CREATE TABLE NODES (
  wc_id INTEGER NOT NULL REFERENCES WCROOT (id), local_relpath TEXT NOT NULL, op_depth INTEGER NOT NULL,
  parent_relpath TEXT, repos_id INTEGER REFERENCES REPOSITORY (id), repos_path TEXT, revision INTEGER,
  presence TEXT NOT NULL, moved_here INTEGER, moved_to TEXT, kind TEXT NOT NULL, properties BLOB, depth TEXT,
  checksum TEXT REFERENCES PRISTINE (checksum), symlink_target TEXT, changed_revision INTEGER,
  changed_date INTEGER, changed_author TEXT, translated_size INTEGER, last_mod_time INTEGER,
  dav_cache BLOB, file_external INTEGER, inherited_props BLOB,
  PRIMARY KEY (wc_id, local_relpath, op_depth));
CREATE UNIQUE INDEX I_NODES_PARENT ON NODES (wc_id, parent_relpath, local_relpath, op_depth);
CREATE UNIQUE INDEX I_NODES_MOVED ON NODES (wc_id, moved_to, op_depth);
CREATE VIEW NODES_CURRENT AS SELECT * FROM nodes AS n WHERE op_depth =
  (SELECT MAX(op_depth) FROM nodes AS n2 WHERE n2.wc_id = n.wc_id AND n2.local_relpath = n.local_relpath);
CREATE VIEW NODES_BASE AS SELECT * FROM nodes WHERE op_depth = 0;
CREATE TRIGGER nodes_insert_trigger AFTER INSERT ON nodes WHEN NEW.checksum IS NOT NULL BEGIN
  UPDATE pristine SET refcount = refcount + 1 WHERE checksum = NEW.checksum; END;
CREATE TRIGGER nodes_delete_trigger AFTER DELETE ON nodes WHEN OLD.checksum IS NOT NULL BEGIN
  UPDATE pristine SET refcount = refcount - 1 WHERE checksum = OLD.checksum; END;
CREATE TRIGGER nodes_update_checksum_trigger AFTER UPDATE OF checksum ON nodes WHEN NEW.checksum IS NOT OLD.checksum BEGIN
  UPDATE pristine SET refcount = refcount + 1 WHERE checksum = NEW.checksum;
  UPDATE pristine SET refcount = refcount - 1 WHERE checksum = OLD.checksum; END;
CREATE TABLE EXTERNALS (
  wc_id INTEGER NOT NULL REFERENCES WCROOT (id), local_relpath TEXT NOT NULL, parent_relpath TEXT NOT NULL,
  repos_id INTEGER NOT NULL REFERENCES REPOSITORY (id), presence TEXT NOT NULL, kind TEXT NOT NULL,
  def_local_relpath TEXT NOT NULL, def_repos_relpath TEXT NOT NULL, def_operational_revision TEXT,
  def_revision TEXT, PRIMARY KEY (wc_id, local_relpath));
CREATE UNIQUE INDEX I_EXTERNALS_DEFINED ON EXTERNALS (wc_id, def_local_relpath, local_relpath);
`
