"""Pure/fake-executor checks only. No Docker, PostgreSQL, psql or network."""
import copy
import importlib.util
import json
from pathlib import Path
import sys
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("registration_provision_under_test", Path(__file__).with_name("pg-registration-provision.py"))
P = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = P
SPEC.loader.exec_module(P)
DB = "wt_test_abcdefghijklmnop"
ADMIN = "wt_admin_abcdefghijklmnop"
PASSWORDS = {r:"fake_only_"+str(i)*40 for i,r in enumerate(P.LOGIN_ROLES)}


def fake_catalog(bundle):
    """Invented PG-shaped fixture, never claimed to be server acceptance."""
    c = bundle.contract
    rows = []
    def add(kind, **values):
        row = {"kind":kind, **values}
        if set(row) != set(P.FIELDS[kind].split()) | {"kind"}:
            raise AssertionError((kind,set(P.FIELDS[kind].split())-set(row),set(row)-set(P.FIELDS[kind].split())-{'kind'}))
        rows.append(row)
    add("identity", database=DB,database_oid=70001,database_owner=P.OWNER,schema_oid=70002,schema_owner=P.OWNER,version=P.VERSION,session_user=P.OWNER,current_user=P.OWNER,activity_admitted=True,unexpected_schemas=0,public_objects=0,default_acls=0,role_settings=0,event_triggers=0,extensions=["plpgsql"],rewrites=0,policies=0)
    for i,r in enumerate(P.ROLES):
        add("role",name=r,oid=70003+i,superuser=False,createdb=False,createrole=False,replication=False,bypassrls=False,inherit=False,login=r in P.LOGIN_ROLES,connection_limit=0 if r==P.ACTIVITY_GUARD else 2,valid_until=None,config=None)
    for membership in P.expected_membership_rows({r:70003+i for i,r in enumerate(P.ROLES)}):
        add("membership", **{k:v for k,v in membership.items() if k != "kind"})
    for name,oid in P.BUILTIN_STATS_ROLES.items():
        add("builtin_role",name=name,oid=oid,superuser=False,createdb=False,createrole=False,replication=False,bypassrls=False,login=False)
    add("bootstrap", name=ADMIN, oid=10, superuser=True, login=True)
    add("admission_schema", name=P.ADMISSION_SCHEMA, oid=70020, owner=ADMIN, owner_oid=10, other_objects=0)
    for table,cols in c["tables"].items():
        for n,(name,info) in enumerate(cols.items(),1):
            add("column",table=table,name=name,position=n,type={"timestamptz":"timestamp with time zone","epoch_ms":P.SCHEMA+".epoch_ms"}.get(info["type"],info["type"]),not_null=info["not_null"],identity=info["identity"],generated=info["generated"],default=info["default_source"] or None,dropped=False,collation="default" if info["type"] in ("text","text[]") else None)
    for i,(name,e) in enumerate(c["functions"].items()):
        add("function",name=name,oid=71000+i,owner=e["owner"],body=e["body"],definition=f"CREATE OR REPLACE FUNCTION {P.SCHEMA}.{name}() AS $function${e['body']}$function$\n",arguments=", ".join(e["args"]),argument_names=e["argnames"] or None,argument_modes=None,argument_defaults=0,all_argument_types=None,argument_default_expression=None,transform_types=None,result=e["result"],security_definer=e["security_definer"],language="plpgsql",volatility="v",parallel="u",strict=False,leakproof=False,function_kind="f",set_returning=False,config=e["config"] or None,support=0,variadic=0,cost=100,rows=0,binary=None)
    name,e=P.ADMISSION_FUNCTION,c["admission"]
    add("admission_function",name=name,oid=72000,owner=e["owner"],body=e["body"],definition=f"CREATE OR REPLACE FUNCTION {P.ADMISSION_SCHEMA}.{name}() AS $function${e['body']}$function$\n",arguments=", ".join(e["args"]),argument_names=e["argnames"] or None,argument_modes=None,argument_defaults=0,all_argument_types=None,argument_default_expression=None,transform_types=None,result=e["result"],security_definer=e["security_definer"],language="plpgsql",volatility="v",parallel="u",strict=False,leakproof=False,function_kind="f",set_returning=False,config=e["config"] or None,support=0,variadic=0,cost=100,rows=0,binary=None)
    for scope,obj,owner,grantee,priv in (("schema",P.ADMISSION_SCHEMA,ADMIN,ADMIN,"USAGE"),
        ("schema",P.ADMISSION_SCHEMA,ADMIN,ADMIN,"CREATE"),("schema",P.ADMISSION_SCHEMA,ADMIN,P.OWNER,"USAGE"),
        ("function",P.ADMISSION_FUNCTION,P.ACTIVITY_GUARD,P.ACTIVITY_GUARD,"EXECUTE"),
        ("function",P.ADMISSION_FUNCTION,P.ACTIVITY_GUARD,P.OWNER,"EXECUTE")):
        add("admission_acl", scope=scope,object=obj,column="",owner=owner,grantee=grantee,grantor=owner,privilege=priv,grantable=False)
    for e in c["triggers"]:
        add("trigger",table=e["table"],name=e["name"],function=e["function"],function_schema=P.SCHEMA,trigger_kind=e["kind"],enabled="O",arguments=0,argument_bytes="",qualification=None,columns="",constraint=0,deferrable=False,deferred=False,old_table=None,new_table=None)
    constraints = P.expected_constraints(c)
    indexes = copy.deepcopy(c["indexes"])
    for i,e in enumerate(constraints):
        name = e["catalog_name"]
        idx = name if e["kind"] in ("p","u") else None
        add("constraint",table=e["table"],name=name,constraint_kind=e["kind"],keys=list(e["keys"]),ref_table=e["ref_table"] or None,ref_schema=P.SCHEMA if e["kind"]=="f" else None,ref_keys=list(e["ref_keys"]),deferrable=e["deferrable"],deferred=e["deferred"],validated=True,enforced=True,local=True,inherit_count=0,no_inherit={"c":False,"p":True,"u":True,"f":True,"n":False}[e["kind"]],match="s" if e["kind"]=="f" else " ",update_action="a" if e["kind"]=="f" else " ",delete_action="a" if e["kind"]=="f" else " ",definition=e["source"],index=idx)
        if idx:
            indexes.append(dict(name=idx,table=e["table"],keys=e["keys"],options=(0,)*len(e["keys"]),unique=True,primary=e["kind"]=="p",predicate_source=""))
        if e["kind"]=="f":
            for table,fn,kind in ((e["table"],"RI_FKey_check_ins",5),(e["table"],"RI_FKey_check_upd",17),(e["ref_table"],"RI_FKey_noaction_del",9),(e["ref_table"],"RI_FKey_noaction_upd",17)):
                add("internal_trigger",table=table,constraint_table=e["table"],constraint_name=name,function=fn,function_schema="pg_catalog",trigger_kind=kind,enabled="O",deferrable=e["deferrable"],deferred=e["deferred"],arguments=0,argument_bytes="",qualification=None,columns="")
    for e in indexes:
        add("index",name=e["name"],table=e["table"],keys=list(e["keys"]),options=" ".join(map(str,e["options"])),unique=e["unique"],primary=e.get("primary",False),valid=True,ready=True,live=True,nulls_not_distinct=False,key_count=len(e["keys"]),attribute_count=len(e["keys"]),method="btree",expressions=None,predicate=e["predicate_source"] or None,definition=f"CREATE {'UNIQUE ' if e['unique'] else ''}INDEX {e['name']} ON {P.SCHEMA}.{e['table']} USING btree ({', '.join(key + (' DESC' if option == 3 else '') for key,option in zip(e['keys'],e['options']))})")
    for name,kind in list((x,"r") for x in c["tables"]) + [(x["name"],"i") for x in indexes] + [("outbox_id_seq","S")]:
        add("relation",name=name,relation_kind=kind,owner=P.OWNER,persistence="p",rls=False,force_rls=False,partition=False,options=None)
    for name,kind,rel,elem,base in [("epoch_ms","d",None,None,"int8"),("_epoch_ms","b",None,"epoch_ms",None)] + [(t,"c",t,None,None) for t in c["tables"]] + [("_"+t,"b",None,t,None) for t in c["tables"]]:
        add("type",name=name,type_kind=kind,owner=P.OWNER,relation=rel,element=elem,base_type=base,not_null=False,default=None)
    add("sequence",name="outbox_id_seq",data_type="bigint",start=1,increment=1,min=1,max=9223372036854775807,cache=1,cycle=False,table="outbox",column="id",dependency="i")
    for scope,obj,col,grantee,priv in c["acls"]:
        owner=P.ROUTINE if scope=="function" and obj==P.CONSUME else P.OWNER
        add("acl",scope=scope,object=obj,column=col,owner=owner,grantee=grantee,grantor=owner,privilege=priv,grantable=False)
    for scope,objects,privs in (("table",c["tables"],"INSERT SELECT UPDATE DELETE TRUNCATE REFERENCES TRIGGER MAINTAIN"),("sequence",["outbox_id_seq"],"SELECT UPDATE USAGE"),("function",c["functions"],"EXECUTE"),("schema",[P.SCHEMA],"CREATE USAGE"),("database",[""],"CREATE CONNECT TEMPORARY")):
        for obj in objects:
            owner=P.ROUTINE if scope=="function" and obj==P.CONSUME else P.OWNER
            for priv in privs.split():
                add("acl",scope=scope,object=obj,column="",owner=owner,grantee=owner,grantor=owner,privilege=priv,grantable=False)
    for table in c["tables"]:
        add("row_count",table=table,count=1 if table in ("tenants","users","developer_apps","scope_limits") else 0)
    add("seed",exact=True)
    return rows


class FakeExecutor:
    def __init__(self,bundle,fail_phase=None,catalog=None):
        self.bundle=bundle;self.calls=[];self.fail_phase=fail_phase;self.catalog=catalog
    def execute(self,step):
        self.calls.append(step)
        if step.phase==self.fail_phase:
            raise RuntimeError("private credential / SQL must not escape")
        if step.phase=="bootstrap_preflight":
            return [dict(version=P.VERSION,session_user=ADMIN,current_user=ADMIN,database_absent=True,roles_absent=True,activity_admitted=True,bootstrap_oid=10,bootstrap_superuser=True,event_triggers=0)]
        if step.phase=="bootstrap_helper":
            return [{"guard_password_is_null":True}]
        if step.phase=="catalog_readback":
            return copy.deepcopy(self.catalog) if self.catalog is not None else fake_catalog(self.bundle)
        return []


class ProvisionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bundle=P.load_and_verify_sources(ROOT)
        cls.plan=P.build_provision_plan(cls.bundle,DB,ADMIN,PASSWORDS)
        cls.catalog=fake_catalog(cls.bundle)
        cls.receipts=[dict(phase=s.phase,login_role=s.login_role,database=s.database,sql_sha256=P.sha(s.sql.encode()),acknowledged=True) for s in cls.plan]

    def test_import_performs_no_file_or_resource_actions(self):
        code=Path(SPEC.origin).read_text()
        with mock.patch("pathlib.Path.read_bytes",side_effect=AssertionError("import read")), mock.patch("builtins.open",side_effect=AssertionError("import open")):
            exec(compile(code,SPEC.origin,"exec"),{"__name__":SPEC.name,"__file__":SPEC.origin})

    def test_source_inventory_is_derived(self):
        c=self.bundle.contract
        self.assertEqual((len(c['tables']),sum(map(len,c['tables'].values())),len(c['functions']),len(c['triggers']),len(c['constraints']),len(c['indexes']),len(c['acls'])),(37,440,20,24,354,41,162))
        self.assertEqual(len(c['tables']['registration_prepared_grants']),57)

    def test_reviewed_sources_reject_byte_mutation(self):
        bad=P.SourceBundle(tuple((p,t+'\n' if i==0 else t) for i,(p,t) in enumerate(self.bundle.texts)),self.bundle.sql_manifest_sha256,self.bundle.contract)
        with self.assertRaisesRegex(P.ProvisionError,'SOURCE_HASH_MISMATCH'):
            P.build_provision_plan(bad,DB,ADMIN,PASSWORDS)

    def test_contract_tamper_rejected_before_executor(self):
        bad=copy.deepcopy(self.bundle);bad.contract['tables'].pop('tenants')
        x=FakeExecutor(bad)
        with self.assertRaisesRegex(P.ProvisionError,'SOURCE_CONTRACT_MISMATCH'):
            P.provision(x,bad,DB,ADMIN,PASSWORDS)
        self.assertEqual(x.calls,[])

    def test_baseline_atomic_boundaries_and_actual_owner(self):
        self.assertEqual(len(self.plan),19)
        for s in self.plan[5:16]:
            self.assertTrue(s.sql.startswith('BEGIN;\nSET LOCAL search_path TO wt_synthetic_registry, pg_catalog, pg_temp;'))
            self.assertTrue(s.sql.endswith('\nCOMMIT;\n'))
            self.assertEqual(s.login_role,P.OWNER)
        boundary=self.plan[16]
        self.assertTrue(boundary.sql.endswith(dict(self.bundle.texts)[P.BOUNDARY_PATH]))
        self.assertEqual(sum(x=='BEGIN' for x in P._split(boundary.sql)),1)
        self.assertNotIn('SET ROLE wt_registration_lab_owner', ''.join(s.sql for s in self.plan))
        self.assertEqual(self.plan[2].sql, f'CREATE DATABASE {DB} OWNER {P.OWNER} TEMPLATE template0 ENCODING \'UTF8\';\n')

    def test_role_password_and_target_rejections(self):
        for database,admin,passwords in [("postgres",ADMIN,PASSWORDS),(DB,"postgres",PASSWORDS),(DB,ADMIN,{**PASSWORDS,P.CONSUMER:"x' OR true"}),(DB,ADMIN,{**PASSWORDS,P.CONSUMER:PASSWORDS[P.OWNER]}),(DB,ADMIN,{**PASSWORDS,P.ROUTINE:'x'*40})]:
            with self.subTest(database=database,admin=admin),self.assertRaises(P.ProvisionError):
                P.build_provision_plan(self.bundle,database,admin,passwords)

    def test_bootstrap_no_secret_repr_or_adoption(self):
        step=self.plan[1]
        for password in PASSWORDS.values():
            self.assertIn(password,step.sql);self.assertNotIn(password,repr(step))
        self.assertNotIn('IF NOT EXISTS',step.sql)
        self.assertNotIn('SET ROLE',step.sql)
        self.assertIn('WITH ADMIN FALSE, INHERIT FALSE, SET TRUE',step.sql)
        self.assertIn('CREATE ROLE wt_registration_lab_routine NOLOGIN',step.sql)

    def test_seed_is_minimal_disabled_and_inert(self):
        seed=P.seed_sql(DB,self.bundle,ADMIN)
        self.assertEqual(seed.count('INSERT INTO'),4)
        self.assertIn("'!disabled-inert-synthetic-not-a-password!',false",seed)
        self.assertIn("'!inert-synthetic-no-auth-token!',true,0,0,'[]'::jsonb,NULL",seed)
        for forbidden in ['INSERT INTO wt_synthetic_registry.callback','CREATE ROLE','GRANT ','service_key','worker_key']:
            self.assertNotIn(forbidden,seed)

    def test_fake_full_provision_and_private_redaction(self):
        x=FakeExecutor(self.bundle)
        receipt=P.provision(x,self.bundle,DB,ADMIN,PASSWORDS)
        self.assertEqual(len(x.calls),19)
        self.assertEqual(receipt.public_evidence['acceptance_tests'],'NOT_RUN')
        self.assertEqual(receipt.public_evidence['constraint_semantic_normalization'],'NOT_PROVEN')
        self.assertEqual(receipt.private_metadata['routine_oid'], next(r['oid'] for r in self.catalog if r['kind']=='function' and r['name']==P.CONSUME))
        self.assertEqual(receipt.private_metadata['routine_definition_sha256'],P.sha(next(r['definition'] for r in self.catalog if r['kind']=='function' and r['name']==P.CONSUME).encode()))
        for secret in PASSWORDS.values():
            self.assertNotIn(secret,repr(receipt));self.assertNotIn(secret,json.dumps(receipt.public_evidence));self.assertNotIn(secret,json.dumps(receipt.private_catalog_receipt))
        self.assertNotIn('observed_catalog',receipt.public_evidence)
        self.assertLess(len(P.canonical(self.catalog)),P.MAX_RESULT_BYTES)
        self.assertLess(len(self.catalog),4096)

    def test_all_step_failures_stop_without_retry(self):
        for s in self.plan:
            with self.subTest(phase=s.phase):
                x=FakeExecutor(self.bundle,fail_phase=s.phase)
                with self.assertRaisesRegex(P.ProvisionError,'PROVISION_EXECUTION_FAILED_STOP_NO_RETRY') as error:
                    P.provision(x,self.bundle,DB,ADMIN,PASSWORDS)
                self.assertEqual(x.calls[-1].phase,s.phase)
                self.assertNotIn('credential',str(error.exception))
                self.assertEqual(sum(a.phase==s.phase for a in x.calls),1)

    def test_catalog_requires_ordered_exact_source_execution_receipt(self):
        for change in ('missing','order','source','role','ack'):
            receipts=copy.deepcopy(self.receipts)
            if change=='missing':receipts.pop()
            elif change=='order':receipts[4],receipts[5]=receipts[5],receipts[4]
            elif change=='source':receipts[5]['sql_sha256']='0'*64
            elif change=='role':receipts[6]['login_role']=ADMIN
            elif change=='ack':receipts[7]['acknowledged']=False
            with self.subTest(change=change),self.assertRaises(P.ProvisionError):
                P.validate_catalog(self.catalog,self.bundle,DB,receipts)

    def test_catalog_mutation_matrix(self):
        mutations=[('identity','activity_admitted',False),('identity','database_owner',P.CONSUMER),('identity','extensions',['plpgsql','dblink']),('role','superuser',True),('role','inherit',True),('membership','set',False),('column','type','integer'),('column','not_null',False),('column','collation','C'),('function','body','BEGIN RETURN NULL; END'),('function','owner',P.CONSUMER),('function','strict',True),('function','config',['search_path=public']),('trigger','enabled','D'),('trigger','trigger_kind',9),('internal_trigger','enabled','D'),('internal_trigger','function','RI_FKey_cascade_del'),('constraint','validated',False),('constraint','enforced',False),('constraint','keys',['unexpected']),('index','valid',False),('index','method','hash'),('relation','rls',True),('relation','persistence','u'),('type','owner',P.CONSUMER),('sequence','cycle',True),('acl','grantee','PUBLIC'),('acl','grantable',True),('row_count','count',2),('seed','exact',False)]
        for kind,key,value in mutations:
            raw=copy.deepcopy(self.catalog)
            row=next(r for r in raw if r['kind']==kind and (kind!='column' or r['name']=='id'))
            row[key]=value
            with self.subTest(kind=kind,key=key),self.assertRaises(P.ProvisionError):
                P.validate_catalog(raw,self.bundle,DB,self.receipts)

    def test_extra_missing_duplicate_and_unknown_catalogs_rejected(self):
        for kind in P.FIELDS:
            for change in ('remove','duplicate','extra_field'):
                raw=copy.deepcopy(self.catalog);row=next(r for r in raw if r['kind']==kind)
                if change=='remove':raw.remove(row)
                elif change=='duplicate':raw.append(copy.deepcopy(row))
                else:row['unreviewed']='hidden'
                with self.subTest(kind=kind,change=change),self.assertRaises(P.ProvisionError):
                    P.validate_catalog(raw,self.bundle,DB,self.receipts)

    def test_sanitized_catalog_artifact_is_schema_only(self):
        receipt=P.validate_catalog(self.catalog,self.bundle,DB,self.receipts)
        proof=P.sanitized_catalog_proof(receipt)
        self.assertEqual(proof['format'],'worktool-registration-catalog-proof-v1')
        self.assertEqual(proof['constraint_semantic_normalization'],'NOT_PROVEN')
        self.assertTrue(all(row['kind'] not in ('row_count','seed') for row in proof['objects']))
        for row in proof['objects']:
            if row['kind']=='function':
                self.assertNotIn('definition',row)
                self.assertIn('definition_sha256',row)
        serialized=json.dumps(proof)
        for secret in PASSWORDS.values():self.assertNotIn(secret,serialized)
        self.assertNotIn('disabled-inert-synthetic-not-a-password',serialized)
        self.assertNotIn('inert-synthetic-no-auth-token',serialized)
        self.assertLess(len(P.canonical(proof)),P.MAX_RESULT_BYTES)

    def test_postgresql_deparser_wrappers_and_generated_names(self):
        names=P.expected_constraints(self.bundle.contract)
        by_name={c['catalog_name']:c for c in names}
        for name in ('tenants_pkey','tenants_id_not_null','users_pkey','commands_payload_json_object','epoch_ms_check'):
            self.assertTrue(name in by_name,name)
        self.assertTrue(all(len(c['catalog_name'])<=63 for c in names))
        texts=self.bundle.texts + (('expected-names',' '.join(by_name)),)
        examples=[
            "CHECK (((VALUE >= 0) AND (VALUE <= '9007199254740991'::bigint)))",
            "CHECK ((state = ANY (ARRAY['pending_review'::text, 'active'::text, 'revoked'::text])))",
            'CREATE UNIQUE INDEX tenants_pkey ON wt_synthetic_registry.tenants USING btree (id)',
            'NOT NULL id',
            "CHECK ((grant_id COLLATE pg_catalog.\"C\") ~ '^[A-Za-z0-9_-]{1,128}$'::text)",
            "CHECK ((octet_length(intent_bytes) >= 1) AND (octet_length(intent_bytes) <= 32768))",
        ]
        for text in examples:
            with self.subTest(text=text):self.assertEqual(P._safe_definition_text(text,texts),text)

    def test_sanitizer_refuses_unknown_definition_content(self):
        for definition in ("CHECK ('new-private-secret'::text <> '')", "CHECK (unreviewed_identifier)", "CHECK (true) -- hidden comment", "CHECK ('postgresql://secret')", "CHECK (99123456789 > 0)"):
            raw=copy.deepcopy(self.catalog)
            next(r for r in raw if r['kind']=='constraint')['definition']=definition
            receipt=P.validate_catalog(raw,self.bundle,DB,self.receipts)
            with self.subTest(definition=definition),self.assertRaises(P.ProvisionError):
                P.sanitized_catalog_proof(receipt)

    def test_limits_and_queries_are_bounded_metadata_only(self):
        with self.assertRaises(P.ProvisionError):P._bounded_rows(self.catalog*10)
        with self.assertRaises(P.ProvisionError):P._bounded_rows([{'kind':'unknown'}])
        sql=P.catalog_query(self.bundle)
        self.assertNotIn('rolpassword',sql);self.assertNotIn('SELECT *',sql)
        self.assertIn("DO $admission_catalog$",sql)
        self.assertIn("a.allowed IS NOT TRUE",sql)
        self.assertIn('t.tgisinternal',sql)
        self.assertIn('pg_catalog.json_build_array',self.plan[0].sql)

    def test_mutated_definition_is_preserved_not_falsely_semantic_proof(self):
        # Under the explicit trusted-execution model expression equivalence is
        # NOT independently normalized; receipt must preserve that limitation.
        raw=copy.deepcopy(self.catalog)
        row=next(r for r in raw if r['kind']=='constraint' and r['constraint_kind']=='c')
        row['definition']='CHECK (unverified_deparser_text)'
        receipt=P.validate_catalog(raw,self.bundle,DB,self.receipts)
        self.assertEqual(receipt.public_evidence['constraint_semantic_normalization'],'NOT_PROVEN')
        self.assertIn('CHECK (unverified_deparser_text)',json.dumps(receipt.private_catalog_receipt))


class PreflightDiagnosticTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.expected = [dict(version=P.VERSION, session_user=ADMIN, current_user=ADMIN,
            database_absent=True, roles_absent=True, activity_admitted=True, bootstrap_oid=10,
            bootstrap_superuser=True, event_triggers=0)]
        cls.bundle = P.load_and_verify_sources(ROOT)

    def test_exact_match_is_fixed_category_only(self):
        d = P.preflight_field_diagnostics(copy.deepcopy(self.expected), self.expected)
        self.assertEqual(set(d), {'format', 'authority', 'classification', 'shape', 'checks'})
        self.assertEqual(d['format'], 'worktool-registration-preflight-fields-v1')
        self.assertEqual(d['authority'], 'DIAGNOSTIC_ONLY')
        self.assertEqual(d['classification'], 'VALUE_MATCH')
        self.assertEqual(set(d['checks']), set(self.expected[0]))
        self.assertEqual(set(d['shape']), {'root','row_count','row','fields'})
        self.assertEqual(set(d['checks'].values()), {'MATCH'})
        self.assertNotIn(ADMIN, json.dumps(d))
        self.assertNotIn(str(P.VERSION), json.dumps(d))

    def test_every_known_field_mismatch_has_only_its_fixed_key(self):
        for key, value in self.expected[0].items():
            raw = copy.deepcopy(self.expected)
            raw[0][key] = (not value if type(value) is bool else value + 1
                          if type(value) is int else 'postgresql://secret-user:private-password@private-host')
            d = P.preflight_field_diagnostics(raw, self.expected)
            with self.subTest(field=key):
                self.assertEqual(d['classification'], 'VALUE_MISMATCH')
                self.assertEqual(d['checks'][key], 'MISMATCH')
                self.assertTrue(all(status == 'MATCH' for name,status in d['checks'].items() if name != key))
                for secret in ('secret-user', 'private-password', 'private-host', ADMIN):
                    self.assertNotIn(secret, json.dumps(d))

    def test_malformed_shapes_and_unknown_keys_are_never_echoed(self):
        secret = 'UNREVIEWED_SQL_PASSWORD_DSN'
        cases = [None, False, 1, secret, {}, [], [None], [secret], [[], {}],
                 [self.expected[0], self.expected[0]],
                 [{**self.expected[0], secret: secret}],
                 [{k:v for k,v in self.expected[0].items() if k != 'version'}]]
        for raw in cases:
            d = P.preflight_field_diagnostics(raw, self.expected)
            self.assertEqual(d['classification'], 'INVALID_SHAPE')
            self.assertNotIn(secret, json.dumps(d))
            self.assertNotIn(ADMIN, json.dumps(d))
            self.assertLess(len(json.dumps(d)), 1024)

    def test_all_field_type_errors_are_redacted(self):
        for key in self.expected[0]:
            for value in (None, [], {}, 'private-password' if type(self.expected[0][key]) is not str else 7):
                raw = copy.deepcopy(self.expected)
                raw[0][key] = value
                d = P.preflight_field_diagnostics(raw, self.expected)
                with self.subTest(key=key, value_type=type(value).__name__):
                    self.assertEqual(d['classification'], 'INVALID_SHAPE')
                    self.assertEqual(d['checks'][key], 'INVALID_SHAPE')
                    self.assertNotIn('private-password', json.dumps(d))

    def test_preflight_rejects_python_boolean_integer_alias(self):
        # Diagnostics remain non-authoritative; the admission predicate itself
        # now rejects Python's True == 1 alias rather than inheriting it.
        raw = copy.deepcopy(self.expected)
        raw[0]['activity_admitted'] = 1
        self.assertTrue(raw == self.expected)
        self.assertFalse(P._exact_preflight_match(raw,self.expected))
        self.assertEqual(P.preflight_field_diagnostics(raw, self.expected)['classification'], 'INVALID_SHAPE')

    def test_sink_failure_cannot_change_existing_success_or_error(self):
        expected = self.expected
        class Sink(FakeExecutor):
            def __init__(self, bundle, result):
                super().__init__(bundle)
                self.result = result
                self.diagnostics = []
            def execute(self, step):
                if step.phase == 'bootstrap_preflight':
                    self.calls.append(step)
                    return self.result
                return super().execute(step)
            def record_preflight_diagnostics(self, diagnostic):
                self.diagnostics.append(diagnostic)
                raise RuntimeError('private password SQL error must not escape')
        good = Sink(self.bundle, copy.deepcopy(expected))
        receipt = P.provision(good, self.bundle, DB, ADMIN, PASSWORDS)
        self.assertEqual(receipt.public_evidence['acceptance_tests'], 'NOT_RUN')
        self.assertEqual(len(good.calls), 19)
        self.assertEqual(len(good.diagnostics), 1)
        bad_result = copy.deepcopy(expected)
        bad_result[0]['activity_admitted'] = False
        bad = Sink(self.bundle, bad_result)
        with self.assertRaisesRegex(P.ProvisionError, '^BOOTSTRAP_NOT_PRISTINE$'):
            P.provision(bad, self.bundle, DB, ADMIN, PASSWORDS)
        self.assertEqual(len(bad.calls), 1)
        self.assertEqual(bad.diagnostics[0]['checks']['activity_admitted'], 'MISMATCH')

    def test_executor_failure_retains_original_fixed_error_and_no_extra_call(self):
        class Sink(FakeExecutor):
            def record_preflight_diagnostics(self, diagnostic):
                raise AssertionError('No value diagnostic without returned result')
        executor = Sink(self.bundle, fail_phase='bootstrap_preflight')
        with self.assertRaisesRegex(P.ProvisionError, '^PROVISION_EXECUTION_FAILED_STOP_NO_RETRY$'):
            P.provision(executor, self.bundle, DB, ADMIN, PASSWORDS)
        self.assertEqual(len(executor.calls), 1)

    def test_acceptance_expression_uses_strict_typed_preflight(self):
        import ast
        tree = ast.parse(Path(SPEC.origin).read_text())
        fn = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == 'provision')
        calls = [n for n in ast.walk(fn) if isinstance(n, ast.Call) and isinstance(n.func, ast.Name)
                 and n.func.id == 'require' and len(n.args) == 2
                 and isinstance(n.args[1], ast.Constant) and n.args[1].value == 'BOOTSTRAP_NOT_PRISTINE']
        self.assertEqual(len(calls), 1)
        self.assertEqual(ast.dump(calls[0].args[0]), ast.dump(ast.parse('_exact_preflight_match(result, expected)', mode='eval').body))


class ActivityCensusTests(unittest.TestCase):
    # Independent fixed-domain expectations, not an observed server snapshot.
    categories = ('NOT_INITIALIZED','ARCHIVER','AUTOVACUUM_LAUNCHER','AUTOVACUUM_WORKER',
        'CLIENT_BACKEND','DEAD_END_CLIENT_BACKEND','BACKGROUND_WORKER','BACKGROUND_WRITER',
        'CHECKPOINTER','IO_WORKER','LOGGER','SLOTSYNC_WORKER','STANDALONE_BACKEND','STARTUP',
        'WALRECEIVER','WALSENDER','WALSUMMARIZER','WALWRITER','LOGICAL_REPLICATION_LAUNCHER',
        'TYPE_NULL','TYPE_OTHER')
    presence_fields = ('role_oid_present','role_name_present','database_oid_present','database_name_present')

    @classmethod
    def wire(cls, counts=None):
        counts = counts or {}
        bins = []
        for category in cls.categories:
            for mask in range(16):
                flags = tuple(bool(mask & bit) for bit in (8,4,2,1))
                bins.append(dict(backend_category=category, **dict(zip(cls.presence_fields,flags)),
                    counted_by_original_predicate=category in ('CLIENT_BACKEND','TYPE_NULL') or flags[1],
                    count=counts.get((category,mask),0)))
        return dict(format='worktool-registration-activity-census-wire-v1',status='OBSERVED',observation=dict(
            scope='PG_STAT_ACTIVITY_CLUSTER_EXCLUDING_SELF', rows_total=sum(b['count'] for b in bins),
            counted_total=sum(b['count'] for b in bins if b['counted_by_original_predicate']),
            not_counted_total=sum(b['count'] for b in bins if not b['counted_by_original_predicate']),bins=bins))

    @classmethod
    def model_sql_projection(cls, rows, self_pid):
        # Test-only model of the reviewed CASE/projection, not a SQL execution
        # claim and never an API for reading real/production activity records.
        import re
        mapping = dict(re.findall(r" WHEN '([^']+)' THEN '([^']+)'", P.activity_census_query()))
        counts = {}
        for row in rows:
            if row['pid'] == self_pid:
                continue
            category = 'TYPE_NULL' if row['backend_type'] is None else mapping.get(row['backend_type'],'TYPE_OTHER')
            mask = sum(bit for name,bit in zip(('usesysid','usename','datid','datname'),(8,4,2,1)) if row[name] is not None)
            counts[category,mask] = counts.get((category,mask),0)+1
        if sum(counts.values()) > 4096:
            return dict(format='worktool-registration-activity-census-wire-v1',status='COUNT_OUT_OF_RANGE',observation=None)
        return cls.wire(counts)

    def assert_rejected(self, value, code='JSON_SHAPE_INVALID'):
        with self.assertRaises(P.ProvisionError) as caught:
            P.validate_activity_census_wire(value)
        self.assertEqual(str(caught.exception),code)

    def test_fixed_single_select_has_no_target_or_execution_argument(self):
        import inspect,re
        self.assertEqual(len(inspect.signature(P.activity_census_query).parameters),0)
        sql = P.activity_census_query()
        self.assertEqual(sql,P.activity_census_query())
        self.assertTrue(sql.startswith('WITH activity AS MATERIALIZED (\n'))
        self.assertEqual(len(P._split(sql)),1)
        self.assertEqual(sql.count(';'),1)
        self.assertEqual(sql.count('FROM pg_catalog.pg_stat_activity'),1)
        self.assertEqual(sql.count('WHERE pid <> pg_catalog.pg_backend_pid()'),1)
        self.assertIn("(backend_type='client backend' OR usename IS NOT NULL OR backend_type IS NULL) AS counted",sql)
        code = '\n'.join(part for kind,part in P._pieces(sql) if kind=='code')
        for word in ('BEGIN','COMMIT','SET','INSERT','UPDATE','DELETE','CREATE','ALTER','GRANT','REVOKE','COPY','CALL','DO'):
            self.assertIsNone(re.search(r'\b'+word+r'\b',code,re.I),word)
        for forbidden in ('SELECT *','pg_authid','rolpassword','client_addr','application_name','query_start','pg_cancel_backend','pg_terminate_backend'):
            self.assertNotIn(forbidden,sql)
        self.assertNotIn('WHERE datid',sql)
        self.assertIn("ELSE CASE WHEN backend_type IS NULL THEN 'TYPE_NULL' ELSE 'TYPE_OTHER' END",sql)
        self.assertEqual(sql.count('CASE WHEN s.rows_total>4096'),2)
        self.assertIn("THEN NULL ELSE pg_catalog.json_build_object(",sql)
        self.assertIn('ORDER BY type_order,presence_order',sql)
        self.assertNotIn('LIMIT ',sql)

    def test_domain_and_sql_literal_inventory_are_exact(self):
        import re
        sql=P.activity_census_query()
        self.assertEqual(P.ACTIVITY_CENSUS_CATEGORIES,self.categories)
        self.assertEqual(P.ACTIVITY_CENSUS_PRESENCE_FIELDS,self.presence_fields)
        self.assertEqual(len(set(self.categories)),21)
        mapping = re.findall(r" WHEN '([^']+)' THEN '([^']+)'",sql)
        self.assertEqual(len(mapping),19)
        self.assertEqual(tuple(code for _,code in mapping),self.categories[:-2])
        self.assertEqual(mapping[-1],('logical replication launcher','LOGICAL_REPLICATION_LAUNCHER'))
        domain=sql.split('type_domain(type_order,backend_category) AS (VALUES\n')[1].split('\n), presence_domain')[0]
        self.assertEqual(re.findall(r"\((\d+),'([^']+)'\)",domain),[(str(i),c) for i,c in enumerate(self.categories)])
        flags=sql.split('presence_domain(presence_order,role_oid_present,role_name_present,database_oid_present,database_name_present) AS (VALUES\n')[1].split('\n), bins')[0]
        self.assertEqual(re.findall(r'\((\d+),(true|false),(true|false),(true|false),(true|false)\)',flags),
            [(str(m),*('true' if m & bit else 'false' for bit in (8,4,2,1))) for m in range(16)])
        self.assertEqual(len(list(P._activity_census_bin_keys())),336)

    def test_exhaustive_type_presence_projection_keeps_original_predicate(self):
        import re
        labels=[x for x,_ in re.findall(r" WHEN '([^']+)' THEN '([^']+)'",P.activity_census_query())]+[None,'unlisted-private-secret']
        rows=[]
        for label in labels:
            for mask in range(16):
                row={'pid':len(rows)+2,'backend_type':label}
                row.update({key:'private-name-or-identifier' if mask & bit else None
                    for key,bit in zip(('usesysid','usename','datid','datname'),(8,4,2,1))})
                rows.append(row)
        projected=self.model_sql_projection(rows,1)
        clean=P.validate_activity_census_wire(projected)
        self.assertEqual(clean['observation']['rows_total'],336)
        self.assertEqual(clean['observation']['counted_total'],184)
        self.assertEqual(clean['observation']['not_counted_total'],152)
        self.assertTrue(all(b['count']==1 for b in clean['observation']['bins']))
        self.assertNotIn('private',json.dumps(clean))

    def test_self_exclusion_unknown_null_and_launchers_are_diagnostics_only(self):
        rows=[dict(pid=5,backend_type='client backend',usesysid=9,usename='secret',datid=8,datname='private')]
        for pid,kind,user,db in [(6,'logical replication launcher','secret',None),(7,None,None,None),
            (8,'private-password-postgresql://example',None,'private'),(9,'logical replication launcher','secret','private')]:
            rows.append(dict(pid=pid,backend_type=kind,usesysid=9 if user else None,usename=user,datid=8 if db else None,datname=db))
        clean=P.validate_activity_census_wire(self.model_sql_projection(rows,5))
        self.assertEqual(clean['observation']['rows_total'],4)
        self.assertEqual(clean['observation']['counted_total'],3)
        nonzero=[b for b in clean['observation']['bins'] if b['count']]
        self.assertEqual(len(nonzero),4)
        self.assertTrue(any(b['backend_category']=='TYPE_NULL' and b['counted_by_original_predicate'] for b in nonzero))
        self.assertTrue(any(b['backend_category']=='TYPE_OTHER' and b['database_name_present'] and not b['counted_by_original_predicate'] for b in nonzero))
        for forbidden in ('secret','private','postgresql://','"pid"','"usesysid"','"usename"','"datid"','"datname"'):
            self.assertNotIn(forbidden,json.dumps(clean))
        self.assertNotIn('PASS',json.dumps(clean))

    def test_zero_bound_and_exact_overflow_sentinel(self):
        for count in (0,1,4096):
            clean=P.validate_activity_census_wire(self.wire({('CLIENT_BACKEND',15):count}))
            self.assertEqual(clean['observation']['rows_total'],count)
            self.assertEqual(len(clean['observation']['bins']),336)
        rows=[dict(pid=2,backend_type='client backend',usesysid=None,usename=None,datid=None,datname=None)]*4097
        wire=self.model_sql_projection(rows,1)
        self.assertEqual(P.validate_activity_census_wire(wire),dict(format='worktool-registration-activity-census-wire-v1',status='COUNT_OUT_OF_RANGE',observation=None))
        for obs in ({},[],self.wire()['observation'],'private secret'):
            self.assert_rejected({**wire,'observation':obs})

    def test_strict_root_and_observation_shapes_never_echo_rejected_data(self):
        secret='UNREVIEWED_PASSWORD_DSN_SQL'
        good=self.wire()
        for value in (None,False,[],[good],secret,{}, {**good,secret:secret},
            {**good,'format':secret},{**good,'status':secret},{**good,'observation':None},
            {**good,'observation':{**good['observation'],secret:secret}},
            {**good,'observation':{**good['observation'],'scope':secret}}):
            self.assert_rejected(value)

    def test_bin_inventory_rejects_extra_missing_duplicate_and_reordered_entries(self):
        good=self.wire()
        for operation in ('missing','extra','duplicate','reverse','extra-key','unknown-category','flag','predicate'):
            bad=copy.deepcopy(good);bins=bad['observation']['bins']
            if operation=='missing':bins.pop()
            elif operation=='extra':bins.append(copy.deepcopy(bins[0]))
            elif operation=='duplicate':bins[1]=copy.deepcopy(bins[0])
            elif operation=='reverse':bins.reverse()
            elif operation=='extra-key':bins[0]['private-secret']='private-secret'
            elif operation=='unknown-category':bins[0]['backend_category']='private-secret'
            elif operation=='flag':bins[0]['database_name_present']=True
            elif operation=='predicate':bins[0]['counted_by_original_predicate']=True
            with self.subTest(operation=operation):self.assert_rejected(bad)

    def test_boolean_fields_require_real_boolean_and_exact_value(self):
        for key in (*self.presence_fields,'counted_by_original_predicate'):
            for value in (0,1,None,'false',[],{}):
                bad=self.wire();bad['observation']['bins'][0][key]=value
                with self.subTest(key=key,value=value):self.assert_rejected(bad)

    def test_counts_require_bounded_real_integer(self):
        for target in ('rows_total','counted_total','not_counted_total','count'):
            for value in (True,False,0.0,'0',None,[],{},-1,4097):
                bad=self.wire();obj=bad['observation']['bins'][0] if target=='count' else bad['observation'];obj[target]=value
                code='COUNT_OUT_OF_RANGE' if type(value) is int else 'JSON_SHAPE_INVALID'
                with self.subTest(target=target,value=value):self.assert_rejected(bad,code)

    def test_totals_are_recomputed_and_not_normalized(self):
        for key in ('rows_total','counted_total','not_counted_total'):
            bad=self.wire({('CLIENT_BACKEND',15):1});bad['observation'][key]+=1
            self.assert_rejected(bad)
        bad=self.wire({('CLIENT_BACKEND',0):4096,('CLIENT_BACKEND',1):1})
        bad['observation'].update(rows_total=4096,counted_total=4096)
        self.assert_rejected(bad,'COUNT_OUT_OF_RANGE')

    def test_clean_reconstruction_and_size_bound(self):
        raw=self.wire({('TYPE_OTHER',15):4096})
        clean=P.validate_activity_census_wire(raw)
        self.assertEqual(clean,raw);self.assertIsNot(clean,raw)
        raw['observation']['bins'][0]['count']=5
        self.assertEqual(clean['observation']['bins'][0]['count'],0)
        self.assertLess(len(P.canonical(clean)),128<<10)
        with mock.patch.object(P,'ACTIVITY_CENSUS_MAX_BYTES',1):
            self.assert_rejected(self.wire(),'JSON_SIZE_LIMIT')

    def test_fixed_nineteen_step_source_plan_hashes(self):
        expected=(
            'b696a89ef8516d792ffce13c5fc7fa0967ba7088fc6ad7158331af307445ef40',
            'a0e0c60a2c5a4204b3e39e6ed85b51c0e4231c7ec3e3cc110e702f2752e8965c',
            '636f190f4973fee0c383fb9f8f790bcbf82af78d99a5adf5345462c617277bf3',
            'c26fc5a70dcd973b58efca125ed591cf68338a5d87aa8365f696bc8025e60c01',
            'f3db1188b7fb140755f572fb4c2a1a5b51922e40e61ba07c41e99e0e90dcbc6f',
            '9876f7a6325d2f8853186e5104fc1e817f21b767b3708d2efcd98750179495a8',
            '0279b53e0f47932423b9146475dfb8a26a875112984e2134f1ce6dae8b17ed55',
            'f5dd0b8656a230cea8ee253221f2184bfa8dac39d2678506b71890950dea8f88',
            'f8fd492a8e5452ecfd4059c96c5e9a58fa060782db1bc83f0d8412282bc893ba',
            '44031d4187413cfe597f6bc255356bdaf7aa568e8e4137b05f8b67d71deeca10',
            '40af898faa55837e759a802cf95ed9b2356f03c75443f52ebbca321a4b712c96',
            'cb18942f79a7bf703deb06aa111e657efb466f8f3b3867e349a9cbcec57771dd',
            '9f649e04074603da39b73f573c2f6f5e27c8d3e196187c4b883957ec47de00bb',
            'd8061b3a5aa0c38d8bb44e386eb58c4f624c96e410cdd73a29cd3b3243d90625',
            'aa47f36ef4e8afc1d850a38adf7e8c4bf64751e0d5bb13c58c474e2369b95f9b',
            '982db91f0984025ba9f44906dcedbac84efa2b45221f41111f5c3dbffbd23f33',
            'c99f6ab3ce5a3ffc793544235925cd8ae8c07fb5cea5fd92e66fc338fefd7550',
            'fee3e64428c75ed5dac1f5fb6b2526d578b518453283098731188d51c5993a9e',
            'd154a1d63f166dd17c7625e7b9068f22139a1c13211f6847895978677b883fd9')
        plan=P.build_provision_plan(P.load_and_verify_sources(ROOT),DB,ADMIN,PASSWORDS)
        self.assertEqual(tuple(P.sha(step.sql.encode()) for step in plan),expected)
        self.assertTrue(all(P.activity_census_query() not in step.sql for step in plan))


class ActivityAdmissionTests(unittest.TestCase):
    """Offline specification/model checks only, not PostgreSQL acceptance."""
    limits = {'checkpointer':1, 'background writer':1, 'walwriter':1,
              'autovacuum launcher':1, 'logical replication launcher':1, 'io worker':3}
    presence = ('usesysid','usename','datid','datname','client_addr','client_hostname','client_port','leader_pid')

    @classmethod
    def setUpClass(cls):
        cls.bundle=P.load_and_verify_sources(ROOT)
        cls.plan=P.build_provision_plan(cls.bundle,DB,ADMIN,PASSWORDS)
        cls.source=dict(cls.bundle.texts)[P.ADMISSION_PATH]
        cls.catalog=fake_catalog(cls.bundle)
        cls.receipts=[dict(phase=s.phase,login_role=s.login_role,database=s.database,
            sql_sha256=P.sha(s.sql.encode()),acknowledged=True) for s in cls.plan]

    @classmethod
    def background(cls, kind, pid=2):
        row={key:None for key in cls.presence}
        row.update(pid=pid,backend_type=kind)
        if kind=='logical replication launcher':row.update(usesysid=10,usename=ADMIN)
        return row

    @classmethod
    def self_row(cls):
        row=cls.background('client backend',1)
        row.update(usesysid=70003,usename=P.OWNER,datid=70001,datname=DB,
                   client_addr='127.0.0.1',client_port=45678)
        return row

    @classmethod
    def model(cls, rows, session=P.OWNER, current=P.ACTIVITY_GUARD, version=180006,
              database=DB, owner_oid=70003, database_oid=70001, bootstrap_oid=10,
              bootstrap_name=ADMIN, schema_owner=10):
        # Independent truth-table specification. No database, no SQL evaluator.
        import re
        if not (session==P.OWNER and current==P.ACTIVITY_GUARD and version==180006
            and re.fullmatch(r'wt_test_[a-z0-9_]{8,48}',database)
            and bootstrap_oid==10 and schema_owner==10
            and re.fullmatch(r'wt_admin_[a-z0-9_]{16,40}',bootstrap_name)):
            return False
        own=[r for r in rows if r['pid']==1]
        if len(own)!=1:return False
        r=own[0]
        if not (r['backend_type']=='client backend' and r['usesysid']==owner_oid
            and r['usename']==P.OWNER and r['datid']==database_oid and r['datname']==database
            and r['leader_pid'] is None):return False
        counts={k:0 for k in cls.limits}
        for r in rows:
            if r['pid']==1:continue
            kind=r['backend_type']
            if kind not in cls.limits:return False
            counts[kind]+=1
            if kind=='logical replication launcher':
                if r['usesysid']!=10 or r['usename']!=bootstrap_name:return False
                fields=cls.presence[2:]
            else:fields=cls.presence
            if any(r[field] is not None for field in fields):return False
        return all(counts[k]<=cap for k,cap in cls.limits.items())

    def reject_catalog(self, raw):
        with self.assertRaises(P.ProvisionError):
            P.validate_catalog(raw,self.bundle,DB,self.receipts)

    def test_source_pin_function_shape_and_no_runtime_expectations(self):
        import re
        self.assertEqual(P.sha(self.source.encode()),P.SOURCE_PINS[P.ADMISSION_PATH])
        self.assertEqual(self.source.count('$registration_activity_guard_v1$'),2)
        body=self.source.split('$registration_activity_guard_v1$')[1]
        self.assertEqual(self.bundle.contract['admission']['body'],body)
        self.assertRegex(self.source,r'CREATE FUNCTION wt_registration_admission\.isolated_owner_session_v1\(\)\s+RETURNS boolean')
        self.assertIn('LANGUAGE plpgsql SECURITY DEFINER VOLATILE PARALLEL UNSAFE',self.source)
        self.assertIn('SET search_path = pg_catalog, pg_temp',self.source)
        self.assertEqual(body.count('FROM pg_catalog.pg_stat_activity'),1)
        self.assertEqual(body.count('PERFORM pg_catalog.pg_stat_clear_snapshot();'),1)
        self.assertLess(body.index('pg_stat_clear_snapshot'),body.index('activity AS MATERIALIZED'))
        self.assertIn('a.allowed IS NOT TRUE',body)
        self.assertIn('RETURN admitted IS TRUE;',body)
        self.assertNotIn('SELECT *',body)
        self.assertNotRegex(body,r'\b(EXECUTE|INSERT|UPDATE|DELETE|COPY|CALL)\b')
        self.assertNotIn('pg_authid',body)
        self.assertNotIn(ADMIN,body)
        self.assertNotIn(DB,body)
        # Every referenced persistent relation/function is explicitly qualified.
        self.assertEqual(re.findall(r'FROM (pg_[a-z_]+)(?:\s|$)',body),[])
        for field in self.presence:self.assertIn('a.'+field,body)
        for kind,cap in self.limits.items():
            self.assertIn("WHERE a.backend_type = '"+kind+"') <= "+str(cap),body)
        for suffix in ('\n','-- changed\n'):
            bad=copy.deepcopy(self.bundle)
            object.__setattr__(bad,'texts',tuple((p,t+suffix if p==P.ADMISSION_PATH else t) for p,t in bad.texts))
            with self.assertRaisesRegex(P.ProvisionError,'SOURCE_HASH_MISMATCH'):
                P.build_provision_plan(bad,DB,ADMIN,PASSWORDS)

    def test_roles_acl_and_install_order_are_narrow(self):
        self.assertEqual(len(self.plan),19)
        self.assertEqual([s.phase for s in self.plan[:5]],['bootstrap_preflight','bootstrap_roles','bootstrap_database','bootstrap_helper','owner_schema'])
        roles=self.plan[1].sql;helper=self.plan[3]
        self.assertIn('CREATE ROLE wt_registration_lab_activity_guard NOLOGIN PASSWORD NULL NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT CONNECTION LIMIT 0;',roles)
        self.assertIn('GRANT pg_read_all_stats TO wt_registration_lab_activity_guard WITH ADMIN FALSE, INHERIT TRUE, SET FALSE;',roles)
        self.assertIn('GRANT wt_registration_lab_routine TO wt_registration_lab_owner WITH ADMIN FALSE, INHERIT FALSE, SET TRUE;',roles)
        self.assertEqual(roles.count('GRANT '),2)
        self.assertEqual(P.LOGIN_ROLES,(P.OWNER,P.ISSUER,P.CONSUMER,P.UNRELATED))
        self.assertEqual(helper.login_role,ADMIN);self.assertEqual(helper.database,DB);self.assertTrue(helper.query)
        self.assertTrue(helper.sql.startswith('BEGIN;'));self.assertTrue(helper.sql.endswith('COMMIT;\n'))
        self.assertIn(self.source,helper.sql)
        self.assertIn('CREATE SCHEMA wt_registration_admission AUTHORIZATION CURRENT_USER;',self.source)
        self.assertIn('REVOKE ALL ON FUNCTION wt_registration_admission.isolated_owner_session_v1() FROM PUBLIC;',self.source)
        self.assertIn('GRANT EXECUTE ON FUNCTION wt_registration_admission.isolated_owner_session_v1() TO wt_registration_lab_owner;',self.source)
        self.assertIn('GRANT USAGE ON SCHEMA wt_registration_admission TO wt_registration_lab_owner;',self.source)
        self.assertNotIn('GRANT OPTION',self.source)
        self.assertIn("'guard_password_is_null',(SELECT rolpassword IS NULL",helper.sql)
        self.assertEqual(sum('pg_authid' in x.sql for x in self.plan),1)
        for step in self.plan:
            self.assertNotIn('DROP ROLE',step.sql)
            self.assertNotIn('ALTER ROLE '+ADMIN,step.sql)
        for step in self.plan[4:]:self.assertNotIn('pg_authid',step.sql)

    def test_bootstrap_uses_same_materialized_classifier_and_verified_directory(self):
        sql=self.plan[0].sql
        self.assertIn(P._activity_classification(self.bundle),sql)
        self.assertIn(P._activity_classification(self.bundle),self.bundle.contract['admission']['body'])
        self.assertEqual(sql.count('FROM pg_catalog.pg_stat_activity'),1)
        self.assertLess(sql.index('pg_stat_clear_snapshot'),sql.index('activity AS MATERIALIZED'))
        self.assertIn("a.datname='postgres' AND a.usename='"+ADMIN+"'",sql)
        self.assertIn('AND r.oid=10 AND d.datdba=10',sql)
        self.assertIn("'bootstrap_oid',(SELECT oid::bigint FROM pg_catalog.pg_roles WHERE rolname='"+ADMIN+"')",sql)
        self.assertIn("pg_catalog.current_setting('shared_preload_libraries')=''",sql)
        self.assertNotIn("backend_type='client backend' OR usename IS NOT NULL",sql)
        self.assertNotIn('min(',sql)

    def test_classifier_accepts_only_listed_normal_shapes_with_upper_limits(self):
        self.assertTrue(self.model([self.self_row()]))
        rows=[self.self_row()]
        for kind,limit in self.limits.items():
            rows.extend(self.background(kind,len(rows)+i+1) for i in range(limit))
        self.assertTrue(self.model(rows))
        for kind in self.limits:
            with self.subTest(overflow=kind):
                self.assertFalse(self.model(rows+[self.background(kind,999)]))
        for kind in self.limits:
            for field in self.presence:
                row=self.background(kind)
                if kind=='logical replication launcher' and field in ('usesysid','usename'):
                    row[field]=None
                else:row[field]='unexpected'
                with self.subTest(kind=kind,field=field):
                    self.assertFalse(self.model([self.self_row(),row]))

    def test_unknown_null_starting_clients_and_second_same_role_always_rejected(self):
        for kind in (None,'unrecognized','not initialized','startup','client backend','dead-end client backend',
                     'autovacuum worker','parallel worker','walsender','walreceiver','background worker','logger','archiver'):
            with self.subTest(kind=kind):self.assertFalse(self.model([self.self_row(),self.background(kind)]))
        peer=self.self_row();peer['pid']=2
        self.assertFalse(self.model([self.self_row(),peer]))
        masked=self.background(None)
        self.assertFalse(self.model([self.self_row(),masked]))
        for oid,name in ((11,ADMIN),(10,'another_admin'),(None,ADMIN),(10,None)):
            row=self.background('logical replication launcher');row.update(usesysid=oid,usename=name)
            self.assertFalse(self.model([self.self_row(),row]))

    def test_self_and_directory_identity_are_fail_closed(self):
        self.assertFalse(self.model([]));self.assertFalse(self.model([self.self_row(),self.self_row()]))
        for key,value in (('backend_type',None),('backend_type','io worker'),('usesysid',10),('usename',ADMIN),
                          ('datid',70002),('datname','postgres'),('leader_pid',9)):
            row=self.self_row();row[key]=value
            with self.subTest(field=key):self.assertFalse(self.model([row]))
        for key,value in (('session',P.CONSUMER),('current',P.OWNER),('version',180005),('database','postgres'),
                          ('owner_oid',90000),('database_oid',90000),('bootstrap_oid',11),
                          ('bootstrap_name','postgres'),('schema_owner',70003)):
            with self.subTest(field=key):self.assertFalse(self.model([self.self_row()],**{key:value}))

    def test_helper_catalog_mutation_matrix(self):
        changes=[('admission_schema','name','public'),('admission_schema','owner',P.OWNER),
            ('admission_schema','owner_oid',70003),('admission_schema','oid',None),('admission_schema','other_objects',1),
            ('bootstrap','name','wt_admin_other_abcdefghijklmnop'),('bootstrap','oid',11),('bootstrap','superuser',False),('bootstrap','login',False),
            ('admission_function','name','other'),('admission_function','oid',None),
            ('admission_function','body','BEGIN RETURN true; END;'),('admission_function','owner',P.OWNER),
            ('admission_function','arguments','text'),('admission_function','argument_names',['x']),
            ('admission_function','argument_defaults',1),('admission_function','all_argument_types',[25]),
            ('admission_function','argument_modes',['o']),('admission_function','argument_default_expression',"'x'::text"),
            ('admission_function','transform_types',[25]),('admission_function','result','record'),
            ('admission_function','security_definer',False),('admission_function','language','sql'),
            ('admission_function','volatility','s'),('admission_function','parallel','s'),
            ('admission_function','strict',True),('admission_function','leakproof',True),
            ('admission_function','function_kind','p'),('admission_function','set_returning',True),
            ('admission_function','config',['search_path=public']),('admission_function','support',123),
            ('admission_function','variadic',25),('admission_function','cost',1),('admission_function','rows',100),
            ('admission_function','binary','unreviewed'),('admission_function','definition','changed'),
            ('admission_acl','grantee','PUBLIC'),('admission_acl','grantable',True),
            ('admission_acl','grantor',P.OWNER),('admission_acl','privilege','CREATE'),
            ('identity','default_acls',1),('identity','role_settings',1)]
        for kind,key,value in changes:
            raw=copy.deepcopy(self.catalog);next(r for r in raw if r['kind']==kind)[key]=value
            with self.subTest(kind=kind,key=key):self.reject_catalog(raw)
        for key,value in (('login',True),('inherit',True),('connection_limit',2),('superuser',True),
                          ('createdb',True),('createrole',True),('replication',True),('bypassrls',True),
                          ('valid_until','infinity'),('config',['search_path=public'])):
            raw=copy.deepcopy(self.catalog);next(r for r in raw if r['kind']=='role' and r['name']==P.ACTIVITY_GUARD)[key]=value
            with self.subTest(guard_attribute=key):self.reject_catalog(raw)
        # Exact fixed source stays independent when observation and receipt body disagree.
        raw=copy.deepcopy(self.catalog);raw.append(copy.deepcopy(next(r for r in raw if r['kind']=='admission_function')))
        raw[-1]['arguments']='text';self.reject_catalog(raw)

    def test_memberships_reject_every_extra_path_and_builtin_mutation(self):
        self.assertEqual(P.BUILTIN_STATS_ROLES,{'pg_monitor':3373,'pg_read_all_settings':3374,'pg_read_all_stats':3375,'pg_stat_scan_tables':3377})
        for member,role in ((P.OWNER,P.ACTIVITY_GUARD),(P.ISSUER,'pg_read_all_stats'),
                            (P.CONSUMER,'pg_monitor'),('unreviewed','pg_monitor'),
                            (P.ROUTINE,P.ACTIVITY_GUARD),(P.UNRELATED,'pg_read_all_stats')):
            raw=copy.deepcopy(self.catalog)
            raw.append(dict(kind='membership',member=member,role=role,member_oid=75000,role_oid=75001,
                            grantor_oid=10,admin=False,inherit=True,set=True))
            with self.subTest(member=member,role=role):self.reject_catalog(raw)
        for member in (P.ACTIVITY_GUARD,'pg_monitor',P.OWNER):
            for key,value in (('admin',True),('inherit',False if member!=P.OWNER else True),
                              ('set',True if member==P.ACTIVITY_GUARD else False),
                              ('grantor_oid',70003),('member_oid',99999),('role_oid',99998)):
                raw=copy.deepcopy(self.catalog);next(r for r in raw if r['kind']=='membership' and r['member']==member)[key]=value
                with self.subTest(member=member,key=key):self.reject_catalog(raw)

    def test_safe_call_validates_source_catalog_before_definer_and_binds_receipt(self):
        receipt=P.validate_catalog(self.catalog,self.bundle,DB,self.receipts)
        meta=receipt.private_metadata
        self.assertEqual(meta['bootstrap_role'],ADMIN);self.assertEqual(meta['bootstrap_oid'],10)
        self.assertEqual(meta['admission_sha256'],P.SOURCE_PINS[P.ADMISSION_PATH])
        sql=P.owner_admission_query(self.bundle,DB,meta)
        self.assertEqual(len(P._split(sql)),2)
        self.assertIn("'activity_admitted'",sql)
        statements=P._split(sql)
        self.assertTrue(statements[0].startswith('DO $admission_catalog$'))
        self.assertNotIn('wt_registration_admission.isolated_owner_session_v1()',statements[0])
        self.assertIn('wt_registration_admission.isolated_owner_session_v1()',statements[1])
        predicate=P.admission_catalog_predicate(self.bundle)
        self.assertIn(predicate,sql)
        for fragment in ('p.prosrc=',"p.proconfig=ARRAY['search_path=pg_catalog, pg_temp']",'p.protrftypes IS NULL',
                         'pg_catalog.pg_default_acl','pg_catalog.pg_db_role_setting','pg_catalog.aclexplode',
                         'pg_catalog.pg_auth_members)=5','EXCEPT VALUES','m.grantor'):
            self.assertIn(fragment,predicate)
        self.assertIn(meta['admission_function_definition_sha256'],sql)
        for key in ('database_oid','schema_oid','admission_schema_oid','admission_function_oid','owner_oid','activity_guard_oid'):
            self.assertIn(str(meta[key]),sql)
        for step in self.plan[4:-1]:
            self.assertIn(predicate,step.sql)
            identity=step.sql.split('DO $identity$')[1].split('END $identity$')[0]
            first,second=identity.split('END IF;',1)
            self.assertNotIn('wt_registration_admission.isolated_owner_session_v1()',first)
            self.assertIn('IF wt_registration_admission.isolated_owner_session_v1() IS NOT TRUE',second)
        self.assertIn(predicate,self.plan[-1].sql)
        self.assertNotIn('pg_authid',sql)
        for key,value in (('bootstrap_role','postgres'),('bootstrap_oid',11),('database_oid',False),
                          ('admission_sha256','0'*64),('admission_function_definition_sha256','not-a-hash')):
            with self.subTest(key=key),self.assertRaises(P.ProvisionError):
                P.owner_admission_query(self.bundle,DB,{**meta,key:value})

    def test_immutable_tamper_has_a_separate_preplanning_rejection_statement(self):
        raw=copy.deepcopy(self.catalog)
        next(r for r in raw if r['kind']=='admission_function')['volatility']='i'
        self.reject_catalog(raw)
        statements=P._split(P.catalog_query(self.bundle))
        self.assertEqual(len(statements),3)
        self.assertTrue(statements[1].startswith('DO $admission_catalog$'))
        self.assertIn("p.provolatile='v'",statements[1])
        self.assertNotIn('wt_registration_admission.isolated_owner_session_v1()',statements[1])
        self.assertIn('wt_registration_admission.isolated_owner_session_v1()',statements[2])
        for name in P.BUILTIN_STATS_ROLES:
            for flag in ('superuser','createdb','createrole','replication','bypassrls','login'):
                raw=copy.deepcopy(self.catalog)
                next(r for r in raw if r['kind']=='builtin_role' and r['name']==name)[flag]=True
                with self.subTest(name=name,flag=flag):self.reject_catalog(raw)

    def test_preflight_every_scalar_type_is_authoritative(self):
        good=FakeExecutor(self.bundle).execute(self.plan[0])
        for key,value in good[0].items():
            substitutes=(int(value),) if type(value) is bool else (float(value), True) if type(value) is int else (1,None)
            for substitute in substitutes:
                raw=copy.deepcopy(good);raw[0][key]=substitute
                class Bad(FakeExecutor):
                    def execute(self,step):
                        self.calls.append(step)
                        return raw
                executor=Bad(self.bundle)
                with self.subTest(field=key,substitute=substitute),self.assertRaisesRegex(P.ProvisionError,'BOOTSTRAP_NOT_PRISTINE'):
                    P.provision(executor,self.bundle,DB,ADMIN,PASSWORDS)
                self.assertEqual(len(executor.calls),1)

    def test_bootstrap_password_boolean_is_exact_and_failure_stops(self):
        for result in ([],[{'guard_password_is_null':False}],[{'guard_password_is_null':1}],
                       [{'guard_password_is_null':None}],[{'guard_password_is_null':True,'extra':'private'}]):
            class Bad(FakeExecutor):
                def execute(self, step):
                    if step.phase=='bootstrap_helper':self.calls.append(step);return result
                    return super().execute(step)
            executor=Bad(self.bundle)
            with self.subTest(shape=result),self.assertRaisesRegex(P.ProvisionError,'ACTIVITY_GUARD_PASSWORD_NOT_NULL'):
                P.provision(executor,self.bundle,DB,ADMIN,PASSWORDS)
            self.assertEqual(executor.calls[-1].phase,'bootstrap_helper')
            self.assertEqual(len(executor.calls),4)


class OIDWireTypeTests(unittest.TestCase):
    """PG18.6 OID JSON encoding regression checks, without a database.

    json_categorize_type treats OID as JSONTYPE_OTHER, not JSONTYPE_NUMERIC.
    json/jsonb then emit its output as a string. Cast only scalar wire outputs
    to bigint; do not coerce wire inputs or weaken identity/NULL-only checks.
    """
    catalog_oid_expressions={
        'identity':{'database_oid':'d.oid::bigint','schema_oid':'n.oid::bigint'},
        'role':{'oid':'r.oid::bigint'},
        'membership':{'member_oid':'m.member::bigint','role_oid':'m.roleid::bigint','grantor_oid':'m.grantor::bigint'},
        'function':{'oid':'p.oid::bigint','support':'p.prosupport::oid::bigint','variadic':'p.provariadic::oid::bigint'},
        'admission_function':{'oid':'p.oid::bigint','support':'p.prosupport::oid::bigint','variadic':'p.provariadic::oid::bigint'},
        'builtin_role':{'oid':'r.oid::bigint'},
        'bootstrap':{'oid':'r.oid::bigint'},
        'admission_schema':{'oid':'n.oid::bigint','owner_oid':'n.nspowner::bigint'},
        'trigger':{'constraint':'t.tgconstraint::oid::bigint'},
    }

    @classmethod
    def setUpClass(cls):
        cls.bundle=P.load_and_verify_sources(ROOT)
        cls.plan=P.build_provision_plan(cls.bundle,DB,ADMIN,PASSWORDS)
        cls.catalog=fake_catalog(cls.bundle)
        cls.receipts=[dict(phase=s.phase,login_role=s.login_role,database=s.database,
            sql_sha256=P.sha(s.sql.encode()),acknowledged=True) for s in cls.plan]

    def test_all_eighteen_scalar_oid_outputs_are_explicit_bigint(self):
        import ast
        tree=ast.parse(Path(SPEC.origin).read_text())
        query=next(n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name=='catalog_query')
        projected={}
        scalar_oid_fields={'oid','support','variadic','constraint'}
        for node in ast.walk(query):
            if not (isinstance(node,ast.Call) and isinstance(node.func,ast.Name) and node.func.id=='add'
                    and len(node.args)>=2 and isinstance(node.args[0],ast.Constant)
                    and isinstance(node.args[1],ast.Dict)):
                continue
            kind=node.args[0].value
            fields={}
            for key,value in zip(node.args[1].keys,node.args[1].values):
                name=ast.literal_eval(key)
                if name in scalar_oid_fields or name.endswith('_oid'):
                    fields[name]=ast.literal_eval(value)
            if fields:projected[kind]=fields
        self.assertEqual(projected,self.catalog_oid_expressions)
        self.assertEqual(sum(map(len,projected.values())),17)
        sql=P.catalog_query(self.bundle)
        for kind,fields in projected.items():
            line=next(line for line in sql.splitlines() if line.startswith("SELECT pg_catalog.jsonb_build_object('kind','"+kind+"',"))
            for field,expression in fields.items():
                with self.subTest(kind=kind,field=field):
                    self.assertIn("'"+field+"',"+expression+",",line)
                    self.assertTrue(expression.endswith('::bigint'))
                    self.assertNotIn('::integer',expression)
        bootstrap=self.plan[0].sql
        self.assertIn("'bootstrap_oid',(SELECT oid::bigint FROM pg_catalog.pg_roles WHERE rolname='"+ADMIN+"')",bootstrap)
        self.assertNotIn("'bootstrap_oid',(SELECT oid FROM",bootstrap)
        self.assertEqual(bootstrap.count('::bigint'),1)
        # OID arrays remain NULL-only. Neither array/string normalization nor a
        # cast in an identity predicate is used to hide a wire mismatch.
        self.assertIn("'all_argument_types',p.proallargtypes",sql)
        self.assertIn("'transform_types',p.protrftypes",sql)
        self.assertNotIn('::bigint',P.admission_catalog_predicate(self.bundle))
        self.assertNotIn('::bigint',P._activity_classification(self.bundle))

    def test_preflight_json_oid_string_is_invalid_shape_and_stops_at_first_step(self):
        good=FakeExecutor(self.bundle).execute(self.plan[0])
        for bad in ('10','010',10.0,True,False,None,[],{}):
            raw=copy.deepcopy(good);raw[0]['bootstrap_oid']=bad
            diagnostics=P.preflight_field_diagnostics(raw,good)
            self.assertEqual(diagnostics['classification'],'INVALID_SHAPE')
            self.assertEqual(diagnostics['checks']['bootstrap_oid'],'INVALID_SHAPE')
            self.assertEqual({k:v for k,v in diagnostics['checks'].items() if k!='bootstrap_oid'},
                             {k:'MATCH' for k in good[0] if k!='bootstrap_oid'})
            class Bad(FakeExecutor):
                def execute(self,step):
                    self.calls.append(step)
                    return raw
            executor=Bad(self.bundle)
            with self.subTest(value=bad),self.assertRaisesRegex(P.ProvisionError,'^BOOTSTRAP_NOT_PRISTINE$'):
                P.provision(executor,self.bundle,DB,ADMIN,PASSWORDS)
            self.assertEqual([step.phase for step in executor.calls],['bootstrap_preflight'])

    def test_catalog_every_oid_projection_rejects_same_value_json_string(self):
        # An invented numeric catalog is not evidence of PostgreSQL encoding.
        # Model the real OID JSON string at every one of the 17 scalar outputs.
        for kind,fields in self.catalog_oid_expressions.items():
            for field in fields:
                raw=copy.deepcopy(self.catalog)
                row=next(row for row in raw if row['kind']==kind)
                self.assertIs(type(row[field]),int)
                row[field]=str(row[field])
                with self.subTest(kind=kind,field=field),self.assertRaises(P.ProvisionError):
                    P.validate_catalog(raw,self.bundle,DB,self.receipts)

    def test_oid_zero_sentinels_do_not_accept_boolean_float_or_string_aliases(self):
        fields=(('function','support'),('function','variadic'),('admission_function','support'),
                ('admission_function','variadic'),('trigger','constraint'))
        for kind,field in fields:
            for bad in (False,True,0.0,'0',None,-1,1):
                raw=copy.deepcopy(self.catalog)
                row=next(row for row in raw if row['kind']==kind)
                self.assertEqual(row[field],0)
                row[field]=bad
                with self.subTest(kind=kind,field=field,value=bad),self.assertRaises(P.ProvisionError):
                    P.validate_catalog(raw,self.bundle,DB,self.receipts)

    def test_oid_domain_uses_unsigned_32_bit_range_without_wire_coercion(self):
        for oid in (1,10,2147483648,4294967295):self.assertTrue(P._oid(oid))
        for oid in (0,-1,4294967296,'10','4294967295',10.0,True,False,None):
            with self.subTest(oid=oid):self.assertFalse(P._oid(oid))
        # Ensure a valid high-bit OID survives the actual catalog/metadata path
        # as an int rather than relying on PostgreSQL's signed int4 range.
        raw=copy.deepcopy(self.catalog)
        next(row for row in raw if row['kind']=='admission_function')['oid']=4294967295
        receipt=P.validate_catalog(raw,self.bundle,DB,self.receipts)
        self.assertEqual(receipt.private_metadata['admission_function_oid'],4294967295)
        self.assertIs(type(receipt.private_metadata['admission_function_oid']),int)


class CatalogValidationDiagnosticTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bundle=P.load_and_verify_sources(ROOT)
        cls.plan=P.build_provision_plan(cls.bundle,DB,ADMIN,PASSWORDS)

    def test_every_static_code_and_only_existing_catalog_install_checks(self):
        import ast
        tree=ast.parse(Path(SPEC.origin).read_text())
        expected=set()
        for fn in tree.body:
            if isinstance(fn,ast.FunctionDef) and fn.name in ('validate_catalog','validate_function','_bounded_rows'):
                expected.update(node.value for node in ast.walk(fn)
                    if isinstance(node,ast.Constant) and type(node.value) is str
                    and node.value.startswith(('CATALOG_','INSTALL_')))
        self.assertEqual(len(expected),63)
        self.assertEqual(P.CATALOG_VALIDATION_DIAGNOSTIC_CODES,expected|{'UNKNOWN'})
        for code in sorted(expected|{'UNKNOWN'}):
            records=[]
            sink=type('Sink',(),{'record_catalog_validation_diagnostic':lambda _,record:records.append(record)})()
            P._record_catalog_validation_diagnostic(sink,P.ProvisionError(code))
            self.assertEqual(records,[{'format':'worktool-registration-catalog-validation-v1',
                'authority':'DIAGNOSTIC_ONLY','failure_code':code}])
            self.assertLess(len(P.canonical(records[0])),180)

    def test_unknown_exceptions_and_spoofed_codes_cannot_escape(self):
        class Hostile(RuntimeError):
            def __str__(self):raise AssertionError('Never stringify an exception')
        class Derived(P.ProvisionError):pass
        class DerivedString(str):pass
        secret='PRIVATE_PASSWORD_SQL_ROLE_DATABASE_SESSION'
        errors=(RuntimeError(secret),Hostile(secret),RuntimeError('CATALOG_IDENTITY'),
                Derived('CATALOG_IDENTITY'),P.ProvisionError(DerivedString('CATALOG_IDENTITY')),
                P.ProvisionError(),P.ProvisionError('CATALOG_IDENTITY',secret),
                P.ProvisionError('CATALOG_IDENTITY '+secret),P.ProvisionError(secret),
                P.ProvisionError({'CATALOG_IDENTITY':secret}),KeyboardInterrupt(secret))
        for error in errors:
            records=[]
            sink=type('Sink',(),{'record_catalog_validation_diagnostic':lambda _,record:records.append(record)})()
            P._record_catalog_validation_diagnostic(sink,error)
            with self.subTest(error_type=type(error).__name__):
                self.assertEqual(records,[{'format':'worktool-registration-catalog-validation-v1',
                    'authority':'DIAGNOSTIC_ONLY','failure_code':'UNKNOWN'}])
                self.assertNotIn(secret,json.dumps(records))

    def test_failed_validation_preserves_exception_and_all_nineteen_calls(self):
        for error in (P.ProvisionError('CATALOG_IDENTITY'),RuntimeError('PRIVATE_SQL_PASSWORD'),KeyboardInterrupt()):
            class Sink(FakeExecutor):
                def __init__(self,bundle):super().__init__(bundle);self.records=[]
                def record_catalog_validation_diagnostic(self,record):self.records.append(record)
            executor=Sink(self.bundle)
            with mock.patch.object(P,'validate_catalog',side_effect=error):
                with self.assertRaises(BaseException) as caught:
                    P.provision(executor,self.bundle,DB,ADMIN,PASSWORDS)
            self.assertIs(caught.exception,error)
            self.assertEqual(executor.calls,list(self.plan))
            self.assertEqual(executor.records,[{'format':'worktool-registration-catalog-validation-v1',
                'authority':'DIAGNOSTIC_ONLY','failure_code':'CATALOG_IDENTITY' if type(error) is P.ProvisionError else 'UNKNOWN'}])

    def test_sink_failures_cannot_replace_validation_or_change_success(self):
        original=P.ProvisionError('CATALOG_MEMBERSHIP')
        for failure in (RuntimeError('PRIVATE_SINK_ERROR'),KeyboardInterrupt(),SystemExit(23)):
            class Sink(FakeExecutor):
                def record_catalog_validation_diagnostic(self,record):raise failure
            executor=Sink(self.bundle)
            with mock.patch.object(P,'validate_catalog',side_effect=original):
                with self.assertRaises(P.ProvisionError) as caught:
                    P.provision(executor,self.bundle,DB,ADMIN,PASSWORDS)
            self.assertIs(caught.exception,original)
            self.assertEqual(executor.calls,list(self.plan))
        class GetterFailure(FakeExecutor):
            @property
            def record_catalog_validation_diagnostic(self):raise RuntimeError('PRIVATE_GETTER')
        with mock.patch.object(P,'validate_catalog',side_effect=original):
            with self.assertRaises(P.ProvisionError) as caught:
                P.provision(GetterFailure(self.bundle),self.bundle,DB,ADMIN,PASSWORDS)
        self.assertIs(caught.exception,original)
        class Never(FakeExecutor):
            def record_catalog_validation_diagnostic(self,record):raise AssertionError('No success diagnostic')
        receipt=P.provision(Never(self.bundle),self.bundle,DB,ADMIN,PASSWORDS)
        self.assertEqual(receipt.public_evidence['status'],'VERIFIED_TRUSTED_INSTALLATION_PROVENANCE')

    def test_real_catalog_rejection_reports_only_code_and_earlier_failure_has_no_record(self):
        class Sink(FakeExecutor):
            def __init__(self,bundle,**kwargs):super().__init__(bundle,**kwargs);self.records=[]
            def record_catalog_validation_diagnostic(self,record):self.records.append(record)
        raw=fake_catalog(self.bundle)
        next(r for r in raw if r['kind']=='identity')['database']='PRIVATE_DYNAMIC_DATABASE'
        executor=Sink(self.bundle,catalog=raw)
        with self.assertRaisesRegex(P.ProvisionError,'^CATALOG_IDENTITY$'):
            P.provision(executor,self.bundle,DB,ADMIN,PASSWORDS)
        self.assertEqual(executor.records[0]['failure_code'],'CATALOG_IDENTITY')
        self.assertNotIn('PRIVATE_DYNAMIC_DATABASE',json.dumps(executor.records))
        for phase in ('bootstrap_preflight','bootstrap_helper','catalog_readback'):
            executor=Sink(self.bundle,fail_phase=phase)
            with self.assertRaises(P.ProvisionError):P.provision(executor,self.bundle,DB,ADMIN,PASSWORDS)
            self.assertEqual(executor.records,[])

    def test_diagnostic_wrapper_and_reviewed_sql_validation_ast(self):
        import ast
        expected={'build_provision_plan': 'd94ccddf6fbd7bab61182ec57176d8d97711b12951a801b138e0f94f4c8a460a', '_session_guard': 'f55cf7d8eab23979f89ba5a461b23a7dfba7db31e1409ded1b470fed5ec66afa', '_bootstrap_query': '3f47d6eb120c746a5c8baa28076be6ac16ad4c1d7dd08968f6ba383dc2649864', '_admission_catalog_predicate': 'ab432717202915d7715e1469f94d9a4c03b3ab2f08fc234739df043556536967', 'admission_catalog_predicate': '418d56fb0e2a7d2d8a8454cf490fc118e9672636a5b13b26cc32c882eef7b620', 'owner_admission_query': 'b49214efabe09fe6e71c3c653be3633694ce09d96ce5e29a29934f3bdcb26cba', 'catalog_query': '3938f2c476ab7a2e0465b4f2a81a736369e21024a592fce707347f42f5c8aeee', '_bounded_rows': 'ef0ae6801d00e98ddfe4750f3f8af17b913025849478118fc00e2d389001cde3', 'validate_function': 'c27e19b356f75c48f69d480c6c1c75df5a2000a9db376210e5aef4f8a30cc6a2', 'validate_catalog': '1b944d43e3fc077c7c6ff8b02e25d5a9c7143ebdda9d167ef533b8ae4995c012', '_exact_preflight_match': '99347a24fc2c7b072b2293eeb63ca3012835bbecb35e62e5a16204046d0b9bc4', 'provision': 'c1f3d6f7ab0986f06539937ff1a25d5736f5197cc21680ebe77b8674ec6f38f8'}
        tree=ast.parse(Path(SPEC.origin).read_text())
        actual={}
        for fn in tree.body:
            if isinstance(fn,ast.FunctionDef) and fn.name in expected:
                if fn.name=='provision':
                    wrapper=fn.body[-1]
                    self.assertIsInstance(wrapper,ast.Try)
                    self.assertEqual(len(wrapper.body),1)
                    self.assertIsInstance(wrapper.body[0],ast.Return)
                    self.assertEqual(len(wrapper.handlers),1)
                    self.assertIsInstance(wrapper.handlers[0].body[-1],ast.Raise)
                    self.assertIsNone(wrapper.handlers[0].body[-1].exc)
                    fn.body[-1:]=wrapper.body
                actual[fn.name]=P.sha(ast.dump(fn).encode())
        self.assertEqual(actual,expected)


class ConstraintInheritanceTests(unittest.TestCase):
    """Fixed PG18.6/source-DDL expectations, never learned from catalog output.

    REL_18_6: catalog/index.c:index_constraint_create sets noinherit=true when
    there is no parent constraint; commands/tablecmds.c:addFkConstraint does so
    for ordinary tables. nodes/makefuncs.c:makeNotNullConstraint initializes
    false, also used for PK/IDENTITY-generated NOT NULL. parser/gram.y defaults
    opt_no_inherit to false. commands/typecmds.c stores domain CHECK with false.
    """
    expected_by_kind={'c':False,'p':True,'u':True,'f':True,'n':False}

    @classmethod
    def setUpClass(cls):
        cls.bundle=P.load_and_verify_sources(ROOT)
        cls.plan=P.build_provision_plan(cls.bundle,DB,ADMIN,PASSWORDS)
        cls.catalog=fake_catalog(cls.bundle)
        cls.receipts=[dict(phase=s.phase,login_role=s.login_role,database=s.database,
            sql_sha256=P.sha(s.sql.encode()),acknowledged=True) for s in cls.plan]
        cls.expected=P.expected_constraints(cls.bundle.contract)

    def assert_inheritance_rejected(self,table,name,value):
        raw=copy.deepcopy(self.catalog)
        next(c for c in raw if c['kind']=='constraint' and c['table']==table and c['name']==name)['no_inherit']=value
        with self.assertRaisesRegex(P.ProvisionError,'^CATALOG_CONSTRAINT_INHERITANCE$'):
            P.validate_catalog(raw,self.bundle,DB,self.receipts)

    def test_source_inventory_has_exact_boolean_for_every_constraint(self):
        from collections import Counter
        self.assertEqual(Counter(c['kind'] for c in self.expected),{'c':216,'p':37,'u':33,'f':69,'n':386})
        self.assertEqual(Counter(c['no_inherit'] for c in self.expected),{False:602,True:139})
        self.assertEqual(len(self.expected),741)
        for c in self.expected:
            self.assertIs(type(c['no_inherit']),bool)
            self.assertIs(c['no_inherit'],self.expected_by_kind[c['kind']])
        # The independently invented fixture does not obtain this flag from the
        # expected contract, so an old all-false model cannot conceal the bug.
        for c in self.catalog:
            if c['kind']=='constraint':self.assertIs(c['no_inherit'],self.expected_by_kind[c['constraint_kind']])
        receipt=P.validate_catalog(self.catalog,self.bundle,DB,self.receipts)
        self.assertEqual(receipt.public_evidence['constraint_count'],741)
        self.assertEqual(receipt.public_evidence['acceptance_tests'],'NOT_RUN')

    def test_each_type_rejects_opposite_boolean_without_not_null_exemption(self):
        for kind,expected in self.expected_by_kind.items():
            c=next(c for c in self.expected if c['kind']==kind and not c['table'].startswith('@'))
            with self.subTest(kind=kind):
                self.assert_inheritance_rejected(c['table'],c['catalog_name'],not expected)
        domain=next(c for c in self.expected if c['table']=='@epoch_ms')
        self.assertIs(domain['no_inherit'],False)
        self.assert_inheritance_rejected(domain['table'],domain['catalog_name'],True)

    def test_each_type_rejects_non_boolean_wire_values(self):
        for kind in self.expected_by_kind:
            c=next(c for c in self.expected if c['kind']==kind)
            for value in (0,1,0.0,1.0,'false','true',None,[],{}):
                with self.subTest(kind=kind,value=value):
                    self.assert_inheritance_rejected(c['table'],c['catalog_name'],value)

    def test_explicit_primary_key_identity_and_domain_defaults_are_independent(self):
        baseline=dict(self.bundle.texts)['backend-go/migrations/0001_base19.sql']
        self.assertIn('id text PRIMARY KEY',baseline)
        self.assertIn('login text NOT NULL',baseline)
        self.assertIn('id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY',baseline)
        self.assertIn('CREATE DOMAIN epoch_ms AS bigint CHECK',baseline)
        expected={(c['table'],c['catalog_name']):c for c in self.expected}
        for key in (('tenants','tenants_id_not_null'),('users','users_login_not_null'),
                    ('outbox','outbox_id_not_null'),('@epoch_ms','epoch_ms_check')):
            with self.subTest(constraint=key):
                self.assertIs(expected[key]['no_inherit'],False)
                self.assert_inheritance_rejected(*key,True)
        # The parent PK is non-inheritable; its implicit NOT NULL is not.
        self.assertIs(expected['tenants','tenants_pkey']['no_inherit'],True)
        self.assertIs(expected['outbox','outbox_pkey']['no_inherit'],True)

    def test_observed_constraint_kind_cannot_choose_the_expected_value(self):
        for kind in self.expected_by_kind:
            raw=copy.deepcopy(self.catalog)
            c=next(c for c in raw if c['kind']=='constraint' and c['constraint_kind']==kind)
            changed='p' if kind!='p' else 'n'
            c['constraint_kind']=changed
            c['no_inherit']=self.expected_by_kind[changed]
            with self.subTest(kind=kind),self.assertRaisesRegex(P.ProvisionError,'^CATALOG_CONSTRAINT_CONTRACT$'):
                P.validate_catalog(raw,self.bundle,DB,self.receipts)
        # Extra expected fields cannot be smuggled into the verified bundle.
        bad=copy.deepcopy(self.bundle)
        bad.contract['constraints'][0]['no_inherit']=True
        with self.assertRaisesRegex(P.ProvisionError,'^SOURCE_CONTRACT_MISMATCH$'):
            P.build_provision_plan(bad,DB,ADMIN,PASSWORDS)

    def test_source_no_inherit_syntax_remains_rejected_and_code_remains_diagnostic(self):
        with self.assertRaisesRegex(P.ProvisionError,'^SOURCE_CHECK_TAIL$'):
            P._constraint('sample','CHECK(value > 0) NO INHERIT')
        contract={'tables':{'sample':{}},'constraints':[]}
        with self.assertRaisesRegex(P.ProvisionError,'^SOURCE_COLUMN_TAIL$'):
            P._column('sample','value text NOT NULL NO INHERIT',contract)
        self.assertIn('CATALOG_CONSTRAINT_INHERITANCE',P.CATALOG_VALIDATION_DIAGNOSTIC_CODES)
        self.assertIn('CATALOG_CONSTRAINT_INHERITANCE',Path(__file__).with_name('pg-registration-acceptance.py').read_text())
        records=[]
        sink=type('Sink',(),{'record_catalog_validation_diagnostic':lambda _,record:records.append(record)})()
        P._record_catalog_validation_diagnostic(sink,P.ProvisionError('CATALOG_CONSTRAINT_INHERITANCE'))
        self.assertEqual(records,[{'format':'worktool-registration-catalog-validation-v1',
            'authority':'DIAGNOSTIC_ONLY','failure_code':'CATALOG_CONSTRAINT_INHERITANCE'}])


class CatalogArtifactTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bundle=P.load_and_verify_sources(ROOT)
        cls.plan=P.build_provision_plan(cls.bundle,DB,ADMIN,PASSWORDS)
        cls.catalog=fake_catalog(cls.bundle)
        cls.receipts=[dict(phase=s.phase,login_role=s.login_role,database=s.database,
            sql_sha256=P.sha(s.sql.encode()),acknowledged=True) for s in cls.plan]

    def test_three_reviewed_desc_indexes_survive_complete_derived_proof_path(self):
        expected={
            'commands_actor':(('tenant','actor','created'),(0,0,3),
                'CREATE INDEX commands_actor ON wt_synthetic_registry.commands USING btree (tenant, actor, created DESC)'),
            'audit_tenant_time':(('tenant','created'),(0,3),
                'CREATE INDEX audit_tenant_time ON wt_synthetic_registry.audit USING btree (tenant, created DESC)'),
            'deletion_device_time':(('tenant','device','created'),(0,0,3),
                'CREATE INDEX deletion_device_time ON wt_synthetic_registry.deletions USING btree (tenant, device, created DESC)'),
        }
        ordered={x['name']:x for x in self.bundle.contract['indexes'] if any(x['options'])}
        self.assertEqual(set(ordered),set(expected))
        for name,(keys,options,definition) in expected.items():
            self.assertEqual((ordered[name]['keys'],ordered[name]['options']),(keys,options))
            self.assertEqual(next(r for r in self.catalog if r['kind']=='index' and r['name']==name)['definition'],definition)
        receipt=P.validate_catalog(self.catalog,self.bundle,DB,self.receipts)
        proof=P.sanitized_catalog_proof(receipt)
        self.assertEqual({r['name']:r['definition'] for r in proof['objects'] if r['kind']=='index' and r['name'] in expected},
                         {name:value[2] for name,value in expected.items()})
        self.assertEqual(proof['constraint_semantic_normalization'],'NOT_PROVEN')
        self.assertEqual(len(proof['objects']),2348)
        self.assertLess(len(P.canonical(proof)),P.MAX_RESULT_BYTES)

    def test_desc_is_derived_from_contract_not_global_or_observed_words(self):
        self.assertNotIn('desc',P._definition_vocabulary((('empty',''),))[0])
        ascending_source='CREATE INDEX commands_actor ON wt_synthetic_registry.commands USING btree (tenant, actor, created)'
        # With no reviewed DESC option/token, the observed word remains forbidden.
        with self.assertRaisesRegex(P.ProvisionError,'^CATALOG_ARTIFACT_UNREVIEWED_IDENTIFIER$'):
            P._safe_definition_text(ascending_source.replace('created)','created DESC)'),
                (('source_without_desc_option',ascending_source),))
        seen=[];original=P._safe_definition_text
        def inspect(value,texts,vocabulary=None):
            if vocabulary is not None and not seen:seen.append(vocabulary)
            return original(value,texts,vocabulary)
        receipt=P.validate_catalog(self.catalog,self.bundle,DB,self.receipts)
        with mock.patch.object(P,'_safe_definition_text',side_effect=inspect):P.sanitized_catalog_proof(receipt)
        self.assertIn('desc',seen[0][0])
        for unknown in ('PRIVATE_UNREVIEWED_ORDER','ASC','NULLS LAST'):
            raw=copy.deepcopy(self.catalog)
            row=next(r for r in raw if r['kind']=='index' and r['name']=='commands_actor')
            row['definition']=row['definition'].replace(' DESC',' '+unknown)
            receipt=P.validate_catalog(raw,self.bundle,DB,self.receipts)
            with self.subTest(token=unknown),self.assertRaisesRegex(P.ProvisionError,'^CATALOG_ARTIFACT_UNREVIEWED_IDENTIFIER$'):
                P.sanitized_catalog_proof(receipt)

    def test_sort_option_and_receipt_contract_tampering_remain_rejected(self):
        for options in ('0 0 0','0 0 1','0 0 2','0 0 7'):
            raw=copy.deepcopy(self.catalog)
            next(r for r in raw if r['kind']=='index' and r['name']=='commands_actor')['options']=options
            with self.subTest(options=options),self.assertRaisesRegex(P.ProvisionError,'^CATALOG_INDEX_CONTRACT$'):
                P.validate_catalog(raw,self.bundle,DB,self.receipts)
        receipt=P.validate_catalog(self.catalog,self.bundle,DB,self.receipts)
        changed=copy.deepcopy(receipt)
        next(x for x in changed.private_catalog_receipt['expected_contract']['indexes'] if x['name']=='commands_actor')['options']=(0,0,0)
        with self.assertRaisesRegex(P.ProvisionError,'^CATALOG_ARTIFACT_RECEIPT_CHANGED$'):
            P.sanitized_catalog_proof(changed)

    def test_artifact_error_classifier_has_only_exact_fixed_helper_codes(self):
        import ast
        tree=ast.parse(Path(SPEC.origin).read_text())
        expected=set()
        for fn in tree.body:
            if isinstance(fn,ast.FunctionDef) and fn.name in ('sanitized_catalog_proof','_safe_definition_text'):
                expected.update(n.value for n in ast.walk(fn) if isinstance(n,ast.Constant)
                    and type(n.value) is str and n.value.startswith('CATALOG_ARTIFACT_'))
        self.assertEqual(len(expected),11)
        self.assertEqual(P.CATALOG_ARTIFACT_DIAGNOSTIC_CODES,expected|{'UNKNOWN'})
        for code in expected|{'UNKNOWN'}:
            self.assertEqual(P.catalog_artifact_diagnostic(P.ProvisionError(code)),{
                'format':'worktool-registration-catalog-artifact-v1','authority':'DIAGNOSTIC_ONLY','failure_code':code})
        class Hostile(RuntimeError):
            def __str__(self):raise AssertionError('Never stringify errors')
        class DerivedError(P.ProvisionError):pass
        class DerivedString(str):pass
        for error in (Hostile('PRIVATE_ERROR'),RuntimeError('CATALOG_ARTIFACT_TOO_LARGE'),
                      DerivedError('CATALOG_ARTIFACT_TOO_LARGE'),P.ProvisionError(DerivedString('CATALOG_ARTIFACT_TOO_LARGE')),
                      P.ProvisionError('CATALOG_ARTIFACT_TOO_LARGE PRIVATE_ERROR'),P.ProvisionError('SOURCE_HASH_MISMATCH'),
                      P.ProvisionError('CATALOG_IDENTITY'),P.ProvisionError('CATALOG_ARTIFACT_TOO_LARGE','PRIVATE_ERROR'),
                      P.ProvisionError(),KeyboardInterrupt('PRIVATE_ERROR')):
            self.assertEqual(P.catalog_artifact_diagnostic(error),{
                'format':'worktool-registration-catalog-artifact-v1','authority':'DIAGNOSTIC_ONLY','failure_code':'UNKNOWN'})


if __name__ == '__main__':
    unittest.main()
