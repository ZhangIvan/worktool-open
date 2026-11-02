import type {DatabaseSync} from 'node:sqlite';
export function developerSchema(db:DatabaseSync){
 const columns=db.prepare('PRAGMA table_info(commands)').all() as any[];
 if(!columns.some(c=>c.name==='actor_type'))db.exec("ALTER TABLE commands ADD COLUMN actor_type TEXT NOT NULL DEFAULT 'user' CHECK(actor_type IN ('user','app'))");
 if(!columns.some(c=>c.name==='app_epoch'))db.exec('ALTER TABLE commands ADD COLUMN app_epoch INTEGER');
 if(!columns.some(c=>c.name==='app_id'))db.exec('ALTER TABLE commands ADD COLUMN app_id TEXT');
 db.exec(`CREATE TABLE IF NOT EXISTS developer_apps(tenant TEXT NOT NULL,id TEXT NOT NULL,creator TEXT NOT NULL,name TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,active INTEGER NOT NULL DEFAULT 1,auth_epoch INTEGER NOT NULL DEFAULT 0,token_expires INTEGER NOT NULL,scopes TEXT NOT NULL,callback_url TEXT,PRIMARY KEY(tenant,id),FOREIGN KEY(tenant,creator) REFERENCES users(tenant,id));
 CREATE TABLE IF NOT EXISTS developer_bindings(tenant TEXT NOT NULL,app TEXT NOT NULL,device TEXT NOT NULL,PRIMARY KEY(tenant,app,device),FOREIGN KEY(tenant,app) REFERENCES developer_apps(tenant,id),FOREIGN KEY(tenant,device) REFERENCES devices(tenant,id));
 CREATE TABLE IF NOT EXISTS developer_messages(tenant TEXT NOT NULL,app TEXT NOT NULL,batch TEXT NOT NULL,message TEXT NOT NULL,child TEXT NOT NULL,command TEXT NOT NULL,key_hash TEXT NOT NULL,request_hash TEXT NOT NULL,PRIMARY KEY(tenant,app,message),UNIQUE(tenant,app,key_hash),UNIQUE(tenant,command),FOREIGN KEY(tenant,app) REFERENCES developer_apps(tenant,id),FOREIGN KEY(tenant,command) REFERENCES commands(tenant,id));
 CREATE TABLE IF NOT EXISTS developer_callbacks(event TEXT PRIMARY KEY,tenant TEXT NOT NULL,app TEXT NOT NULL,command TEXT NOT NULL,status TEXT NOT NULL,body TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL,last_code TEXT,UNIQUE(tenant,command,status),FOREIGN KEY(tenant,app) REFERENCES developer_apps(tenant,id),FOREIGN KEY(tenant,command) REFERENCES commands(tenant,id));`);
 const appColumns=db.prepare('PRAGMA table_info(developer_apps)').all() as any[];
 if(!appColumns.some(c=>c.name==='auth_epoch'))db.exec('ALTER TABLE developer_apps ADD COLUMN auth_epoch INTEGER NOT NULL DEFAULT 0');
 if(!appColumns.some(c=>c.name==='token_expires'))db.exec('ALTER TABLE developer_apps ADD COLUMN token_expires INTEGER NOT NULL DEFAULT 0');
 db.exec(`CREATE TRIGGER IF NOT EXISTS developer_command_actor_insert BEFORE INSERT ON commands WHEN NEW.actor_type='app' BEGIN SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM developer_apps WHERE tenant=NEW.tenant AND id=NEW.app_id AND creator=NEW.actor) THEN RAISE(ABORT,'APP_ACTOR_BINDING') END; END;
 CREATE TRIGGER IF NOT EXISTS developer_command_actor_update BEFORE UPDATE OF actor_type,app_id,actor,tenant ON commands WHEN NEW.actor_type='app' BEGIN SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM developer_apps WHERE tenant=NEW.tenant AND id=NEW.app_id AND creator=NEW.actor) THEN RAISE(ABORT,'APP_ACTOR_BINDING') END; END;`);
}
