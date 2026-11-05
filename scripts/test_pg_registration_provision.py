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
    add("identity", database=DB,database_oid=70001,database_owner=P.OWNER,schema_oid=70002,schema_owner=P.OWNER,version=P.VERSION,session_user=P.OWNER,current_user=P.OWNER,other_clients=0,unexpected_schemas=0,public_objects=0,default_acls=0,role_settings=0,event_triggers=0,extensions=["plpgsql"],rewrites=0,policies=0)
    for i,r in enumerate(P.ROLES):
        add("role",name=r,oid=70003+i,superuser=False,createdb=False,createrole=False,replication=False,bypassrls=False,inherit=False,login=r!=P.ROUTINE,connection_limit=2,valid_until=None,config=None)
    add("membership",member=P.OWNER,role=P.ROUTINE,admin=False,inherit=False,set=True)
    for table,cols in c["tables"].items():
        for n,(name,info) in enumerate(cols.items(),1):
            add("column",table=table,name=name,position=n,type={"timestamptz":"timestamp with time zone","epoch_ms":P.SCHEMA+".epoch_ms"}.get(info["type"],info["type"]),not_null=info["not_null"],identity=info["identity"],generated=info["generated"],default=info["default_source"] or None,dropped=False,collation="default" if info["type"] in ("text","text[]") else None)
    for i,(name,e) in enumerate(c["functions"].items()):
        add("function",name=name,oid=71000+i,owner=e["owner"],body=e["body"],definition=f"CREATE OR REPLACE FUNCTION {P.SCHEMA}.{name}() AS $function${e['body']}$function$\n",arguments=", ".join(e["args"]),argument_names=e["argnames"] or None,argument_modes=None,argument_defaults=0,result=e["result"],security_definer=e["security_definer"],language="plpgsql",volatility="v",parallel="u",strict=False,leakproof=False,function_kind="f",set_returning=False,config=e["config"] or None,support=0,variadic=0,cost=100,rows=0,binary=None)
    for e in c["triggers"]:
        add("trigger",table=e["table"],name=e["name"],function=e["function"],function_schema=P.SCHEMA,trigger_kind=e["kind"],enabled="O",arguments=0,argument_bytes="",qualification=None,columns="",constraint=0,deferrable=False,deferred=False,old_table=None,new_table=None)
    constraints = P.expected_constraints(c)
    indexes = copy.deepcopy(c["indexes"])
    for i,e in enumerate(constraints):
        name = e["catalog_name"]
        idx = name if e["kind"] in ("p","u") else None
        add("constraint",table=e["table"],name=name,constraint_kind=e["kind"],keys=list(e["keys"]),ref_table=e["ref_table"] or None,ref_schema=P.SCHEMA if e["kind"]=="f" else None,ref_keys=list(e["ref_keys"]),deferrable=e["deferrable"],deferred=e["deferred"],validated=True,enforced=True,local=True,inherit_count=0,no_inherit=False,match="s" if e["kind"]=="f" else " ",update_action="a" if e["kind"]=="f" else " ",delete_action="a" if e["kind"]=="f" else " ",definition=e["source"],index=idx)
        if idx:
            indexes.append(dict(name=idx,table=e["table"],keys=e["keys"],options=(0,)*len(e["keys"]),unique=True,primary=e["kind"]=="p",predicate_source=""))
        if e["kind"]=="f":
            for table,fn,kind in ((e["table"],"RI_FKey_check_ins",5),(e["table"],"RI_FKey_check_upd",17),(e["ref_table"],"RI_FKey_noaction_del",9),(e["ref_table"],"RI_FKey_noaction_upd",17)):
                add("internal_trigger",table=table,constraint_table=e["table"],constraint_name=name,function=fn,function_schema="pg_catalog",trigger_kind=kind,enabled="O",deferrable=e["deferrable"],deferred=e["deferred"],arguments=0,argument_bytes="",qualification=None,columns="")
    for e in indexes:
        add("index",name=e["name"],table=e["table"],keys=list(e["keys"]),options=" ".join(map(str,e["options"])),unique=e["unique"],primary=e.get("primary",False),valid=True,ready=True,live=True,nulls_not_distinct=False,key_count=len(e["keys"]),attribute_count=len(e["keys"]),method="btree",expressions=None,predicate=e["predicate_source"] or None,definition=f"CREATE {'UNIQUE ' if e['unique'] else ''}INDEX {e['name']} ON {P.SCHEMA}.{e['table']} USING btree ({', '.join(e['keys'])})")
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
            return [dict(version=P.VERSION,session_user=ADMIN,current_user=ADMIN,database_absent=True,roles_absent=True,other_clients=0,bootstrap_superuser=True,event_triggers=0)]
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
        self.assertEqual(len(self.plan),18)
        for s in self.plan[4:15]:
            self.assertTrue(s.sql.startswith('BEGIN;\nSET LOCAL search_path TO wt_synthetic_registry, pg_catalog, pg_temp;'))
            self.assertTrue(s.sql.endswith('\nCOMMIT;\n'))
            self.assertEqual(s.login_role,P.OWNER)
        boundary=self.plan[15]
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
        seed=P.seed_sql(DB)
        self.assertEqual(seed.count('INSERT INTO'),4)
        self.assertIn("'!disabled-inert-synthetic-not-a-password!',false",seed)
        self.assertIn("'!inert-synthetic-no-auth-token!',true,0,0,'[]'::jsonb,NULL",seed)
        for forbidden in ['INSERT INTO wt_synthetic_registry.callback','CREATE ROLE','GRANT ','service_key','worker_key']:
            self.assertNotIn(forbidden,seed)

    def test_fake_full_provision_and_private_redaction(self):
        x=FakeExecutor(self.bundle)
        receipt=P.provision(x,self.bundle,DB,ADMIN,PASSWORDS)
        self.assertEqual(len(x.calls),18)
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
        mutations=[('identity','other_clients',1),('identity','database_owner',P.CONSUMER),('identity','extensions',['plpgsql','dblink']),('role','superuser',True),('role','inherit',True),('membership','set',False),('column','type','integer'),('column','not_null',False),('column','collation','C'),('function','body','BEGIN RETURN NULL; END'),('function','owner',P.CONSUMER),('function','strict',True),('function','config',['search_path=public']),('trigger','enabled','D'),('trigger','trigger_kind',9),('internal_trigger','enabled','D'),('internal_trigger','function','RI_FKey_cascade_del'),('constraint','validated',False),('constraint','enforced',False),('constraint','keys',['unexpected']),('index','valid',False),('index','method','hash'),('relation','rls',True),('relation','persistence','u'),('type','owner',P.CONSUMER),('sequence','cycle',True),('acl','grantee','PUBLIC'),('acl','grantable',True),('row_count','count',2),('seed','exact',False)]
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
        self.assertIn("backend_type IS NULL",sql)
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
            database_absent=True, roles_absent=True, other_clients=0,
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

    def test_diagnostic_does_not_replace_python_equality_acceptance(self):
        # Preserve the existing predicate exactly, including its Python equality
        # semantics. A diagnostic type warning cannot revoke an existing match.
        raw = copy.deepcopy(self.expected)
        raw[0]['other_clients'] = False
        self.assertTrue(raw == self.expected)
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
        self.assertEqual(len(good.calls), 18)
        self.assertEqual(len(good.diagnostics), 1)
        bad_result = copy.deepcopy(expected)
        bad_result[0]['other_clients'] = 1
        bad = Sink(self.bundle, bad_result)
        with self.assertRaisesRegex(P.ProvisionError, '^BOOTSTRAP_NOT_PRISTINE$'):
            P.provision(bad, self.bundle, DB, ADMIN, PASSWORDS)
        self.assertEqual(len(bad.calls), 1)
        self.assertEqual(bad.diagnostics[0]['checks']['other_clients'], 'MISMATCH')

    def test_executor_failure_retains_original_fixed_error_and_no_extra_call(self):
        class Sink(FakeExecutor):
            def record_preflight_diagnostics(self, diagnostic):
                raise AssertionError('No value diagnostic without returned result')
        executor = Sink(self.bundle, fail_phase='bootstrap_preflight')
        with self.assertRaisesRegex(P.ProvisionError, '^PROVISION_EXECUTION_FAILED_STOP_NO_RETRY$'):
            P.provision(executor, self.bundle, DB, ADMIN, PASSWORDS)
        self.assertEqual(len(executor.calls), 1)

    def test_existing_acceptance_expression_is_unchanged(self):
        import ast
        tree = ast.parse(Path(SPEC.origin).read_text())
        fn = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == 'provision')
        calls = [n for n in ast.walk(fn) if isinstance(n, ast.Call) and isinstance(n.func, ast.Name)
                 and n.func.id == 'require' and len(n.args) == 2
                 and isinstance(n.args[1], ast.Constant) and n.args[1].value == 'BOOTSTRAP_NOT_PRISTINE']
        self.assertEqual(len(calls), 1)
        self.assertEqual(ast.dump(calls[0].args[0]), ast.dump(ast.parse('result == expected', mode='eval').body))


if __name__ == '__main__':
    unittest.main()
