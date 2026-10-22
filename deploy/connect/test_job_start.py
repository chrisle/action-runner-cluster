import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('hook', Path(__file__).with_name('job-start.py'))
hook = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hook)

class HookTest(unittest.TestCase):
    def run_hook(self, event, tokens):
        with tempfile.TemporaryDirectory() as td:
            d = Path(td)
            (d/'config.json').write_text(json.dumps({'host':'http://connect','op_bin':'/tools'}))
            (d/'event').write_text(json.dumps(event))
            env = {'GITHUB_EVENT_PATH':str(d/'event'),'GITHUB_ENV':str(d/'env'),'GITHUB_PATH':str(d/'path')}
            with patch.dict(os.environ,env), patch.object(hook,'fetch',side_effect=[(x,25) for x in tokens]) as fetch, contextlib.redirect_stdout(io.StringIO()):
                hook.main(d)
                if len(tokens)>1: hook.main(d)
            return (d/'env').read_text(),fetch.call_count
    def test_each_job_reads_current_token(self):
        env,calls=self.run_hook({},['old-fixture','new-fixture'])
        self.assertEqual(calls,2)
        self.assertIn('OP_CONNECT_TOKEN=new-fixture',env)
        self.assertIn('OP_SERVICE_ACCOUNT_TOKEN=\n',env)
    def test_fork_has_no_credentials(self):
        event={'pull_request':{'head':{'repo':{'full_name':'fork/project'}},'base':{'repo':{'full_name':'chrisle/project'}}}}
        env,calls=self.run_hook(event,[])
        self.assertEqual(calls,0)
        self.assertNotIn('OP_CONNECT_TOKEN=old',env)
    def test_same_repo_pr_is_trusted(self):
        event={'pull_request':{'head':{'repo':{'full_name':'chrisle/project'}},'base':{'repo':{'full_name':'chrisle/project'}}}}
        env,calls=self.run_hook(event,['fixture'])
        self.assertEqual(calls,1)
    def test_failed_fetch_does_not_publish_credentials(self):
        with tempfile.TemporaryDirectory() as td:
            d=Path(td)
            (d/'config.json').write_text(json.dumps({'host':'http://connect'}))
            (d/'event').write_text('{}')
            with patch.dict(os.environ,{'GITHUB_EVENT_PATH':str(d/'event'),'GITHUB_ENV':str(d/'env')}), patch.object(hook,'fetch',side_effect=RuntimeError('unavailable')):
                with self.assertRaises(RuntimeError):hook.main(d)
            self.assertFalse((d/'env').exists())
    def test_deleted_or_missing_repo_is_not_trusted(self):
        self.assertFalse(hook.trusted_event({'pull_request':{'head':{'repo':None},'base':{'repo':{'full_name':'chrisle/project'}}}}))
        self.assertFalse(hook.trusted_event({'pull_request':{'head':{},'base':{}}}))
    def test_missing_fork_repo_is_not_trusted(self):
        self.assertFalse(hook.trusted_event({'pull_request':{'base':{'repo':{'full_name':'chrisle/project'}}}}))

if __name__=='__main__':unittest.main()
