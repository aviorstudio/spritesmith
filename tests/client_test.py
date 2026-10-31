"""CLI acceptance using a local synthetic API; no provider operations."""
from contextlib import contextmanager
from email.parser import BytesParser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import platform
import socket
import subprocess
import tempfile
import threading
import unittest

BINARY = Path(os.environ.get('SPRITESMITH_TEST_BINARY', 'target/debug/spritesmith'+('.exe' if os.name == 'nt' else ''))).resolve()


@contextmanager
def api(responder):
    calls = []
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass
        def respond(self):
            body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
            calls.append((self.command, self.path, self.headers, body))
            responder(self, calls)
        do_GET = respond
        do_POST = respond
    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f'http://127.0.0.1:{server.server_port}', calls
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def reply(handler, status=200, body=b'synthetic-image', content_type='image/png', **headers):
    handler.send_response(status)
    handler.send_header('Content-Type', content_type)
    handler.send_header('Content-Length', str(len(body)))
    for name, value in headers.items():
        handler.send_header(name, value)
    handler.end_headers()
    handler.wfile.write(body)


class Client(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = dict(os.environ, SPRITESMITH_API_TOKEN='', SPRITESMITH_API_BASE='http://127.0.0.1:1',
                        HOME=str(self.root), XDG_CONFIG_HOME=str(self.root/'config'), APPDATA=str(self.root/'config'))
    def run_cli(self, *args, **env):
        return subprocess.run([str(BINARY), *args], cwd=self.root, env=self.env|env,
                              capture_output=True, text=True, timeout=10)
    def prompt(self, url, *args, **env):
        return self.run_cli('prompt', '--api', url, *args, 'a duck', **env)

    def test_prompt_fields_auth_output_and_model(self):
        for model, expected, format in [('haiku','anthropic/claude-haiku-5.5','png'), ('opus','anthropic/claude-opus-5.5','svg')]:
            with self.subTest(model=model), api(lambda h,_:reply(h, content_type='image/svg+xml; charset=utf-8' if format=='svg' else 'image/png; fixture=yes')) as (url,calls):
                output=f'new/{model}.{format}'
                result=self.prompt(url,'--model',model,'--format',format,'--out',output,'--operation-id','stable_123',SPRITESMITH_API_TOKEN='fixture-token')
                self.assertEqual(result.returncode,0,result.stderr)
                self.assertEqual(result.stdout.strip(),output)
                self.assertEqual((self.root/output).read_bytes(),b'synthetic-image')
                self.assertEqual(len(calls),1)
                method,path,headers,body=calls[0]
                self.assertEqual((method,path),('POST','/v1/prompt'))
                self.assertEqual(headers['Authorization'],'Bearer fixture-token')
                self.assertEqual(headers['Idempotency-Key'],'stable_123')
                message=BytesParser().parsebytes(f"Content-Type: {headers['Content-Type']}\r\n\r\n".encode()+body)
                fields={part.get_param('name',header='content-disposition'):part.get_payload(decode=True).decode() for part in message.walk() if part.get_param('name',header='content-disposition')}
                self.assertEqual(fields,dict(kind='vector',workflow='agent',subject='character',agent_model=expected,prompt='a duck'))

    def test_no_token_has_no_authorization_header(self):
        with api(lambda h,_:reply(h)) as (url,calls):
            result=self.prompt(url)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertNotIn('Authorization',calls[0][2])
            self.assertEqual(len(calls[0][2]['Idempotency-Key']),32)

    def test_existing_file_and_directory_are_rejected_before_submission(self):
        with api(lambda h,_:reply(h)) as (url,calls):
            (self.root/'existing.png').write_bytes(b'keep')
            (self.root/'folder').mkdir()
            for path in ('existing.png','folder','missing-folder/'):
                result=self.prompt(url,'--out',path)
                self.assertNotEqual(result.returncode,0)
            self.assertEqual(calls,[])
            self.assertEqual((self.root/'existing.png').read_bytes(),b'keep')

    @unittest.skipIf(os.name=='nt','Unix symlink fixture')
    def test_dangling_symlink_is_already_occupied(self):
        (self.root/'link.png').symlink_to(self.root/'absent')
        with api(lambda h,_:reply(h)) as (url,calls):
            self.assertNotEqual(self.prompt(url,'--out','link.png').returncode,0)
            self.assertEqual(calls,[])
            self.assertFalse((self.root/'absent').exists())

    def test_file_created_during_request_is_not_overwritten(self):
        def responder(handler,_):
            (self.root/'race.png').write_bytes(b'keep')
            reply(handler)
        with api(responder) as (url,_):
            self.assertNotEqual(self.prompt(url,'--out','race.png').returncode,0)
            self.assertEqual((self.root/'race.png').read_bytes(),b'keep')

    def test_wrong_media_type_and_api_errors_leave_output_free(self):
        for status,body,media in [(200,b'<html>','text/html'),(200,b'{}','application/json'),(400,b'{"error":"fixture rejection"}','application/json'),(401,b'{}','application/json')]:
            with self.subTest(status=status,media=media), api(lambda h,_:reply(h,status,body,media)) as (url,_):
                result=self.prompt(url,SPRITESMITH_API_TOKEN='fixture-secret')
                self.assertNotEqual(result.returncode,0)
                self.assertFalse((self.root/'spritesmith.png').exists())
                self.assertNotIn('fixture-secret',result.stderr)
                if status==401:
                    self.assertIn('SPRITESMITH_API_TOKEN',result.stderr)

    def test_failed_download_removes_partial_file_and_allows_retry(self):
        def responder(handler,calls):
            if len(calls)==1:
                handler.send_response(200)
                handler.send_header('Content-Type','image/png')
                handler.send_header('Content-Length','1000')
                handler.end_headers()
                handler.wfile.write(b'partial')
                handler.wfile.flush()
                handler.connection.shutdown(socket.SHUT_RDWR)
            else:
                reply(handler)
        with api(responder) as (url,calls):
            self.assertNotEqual(self.prompt(url).returncode,0)
            self.assertFalse((self.root/'spritesmith.png').exists())
            self.assertEqual(self.prompt(url).returncode,0)
            self.assertEqual(len(calls),2)

    def test_durable_generation_polls_and_downloads_without_resubmission(self):
        identity='a'*32
        def responder(handler,calls):
            if handler.command=='POST':
                reply(handler,202,json.dumps(dict(id=identity)).encode(),'application/json')
            elif handler.path.endswith('/result?format=svg'):
                reply(handler,200,b'<svg/>','image/svg+xml')
            else:
                reply(handler,200,json.dumps(dict(status='processing' if len(calls)==2 else 'succeeded')).encode(),'application/json')
        with api(responder) as (url,calls):
            result=self.prompt(url,'--format','svg','--operation-id','keep-id')
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(sum(method=='POST' for method,_,_,_ in calls),1)
            self.assertEqual((self.root/'spritesmith.svg').read_bytes(),b'<svg/>')

    def test_uncertain_and_invalid_jobs_are_never_resubmitted(self):
        for state in ('uncertain','failed','unknown'):
            with self.subTest(state=state):
                def responder(handler,_):
                    reply(handler,202 if handler.command=='POST' else 200,json.dumps(dict(id='a'*32) if handler.command=='POST' else dict(status=state)).encode(),'application/json')
                with api(responder) as (url,calls):
                    result=self.prompt(url)
                    self.assertNotEqual(result.returncode,0)
                    self.assertEqual([method for method,_,_,_ in calls],['POST','GET'])
                    self.assertFalse((self.root/'spritesmith.png').exists())
                    if state=='uncertain':
                        self.assertIn('reconciliation',result.stderr)

    def test_redirects_are_checked_on_every_hop(self):
        for location,allowed in [('/download',True),('http://remote.invalid/sprite',False),('http://localhost:2/sprite',False)]:
            def responder(handler,_):
                if handler.path=='/v1/prompt':
                    reply(handler,307,b'',Location=location)
                else:
                    reply(handler)
            with self.subTest(location=location), api(responder) as (url,calls):
                (self.root/'spritesmith.png').unlink(missing_ok=True)
                result=self.prompt(url,SPRITESMITH_API_TOKEN='fixture-secret')
                self.assertEqual(result.returncode==0,allowed,result.stderr)
                self.assertEqual(len(calls),2 if allowed else 1)
                self.assertNotIn('fixture-secret',result.stderr)
                if allowed:
                    self.assertEqual(calls[1][2]['Authorization'],'Bearer fixture-secret')

    def test_redirect_loop_stops(self):
        with api(lambda h,_:reply(h,307,b'',Location='/loop')) as (url,calls):
            result=self.prompt(url)
            self.assertNotEqual(result.returncode,0)
            self.assertLessEqual(len(calls),11)

    def test_cleartext_remote_token_is_refused_before_submission(self):
        result=self.prompt('http://remote.invalid',SPRITESMITH_API_TOKEN='fixture-secret')
        self.assertNotEqual(result.returncode,0)
        self.assertIn('refusing to send',result.stderr)
        self.assertNotIn('fixture-secret',result.stderr)

    def test_argument_validation_never_submits(self):
        with api(lambda h,_:reply(h)) as (url,calls):
            for arguments in [('--token','secret'),('--model','wrong'),('--kind','raster'),('--format','gif'),('--operation-id','not/safe')]:
                self.assertNotEqual(self.prompt(url,*arguments).returncode,0)
            result=self.run_cli('prompt','--api',url,'a duck','--out','wrong.png')
            self.assertEqual(result.returncode,2)
            self.assertIn('before PROMPT',result.stderr)
            self.assertEqual(calls,[])

    def test_help_and_missing_credential_explain_the_fix(self):
        result=self.run_cli('--help')
        self.assertEqual(result.returncode,0)
        for term in ('SPRITESMITH_API_TOKEN','Clerk API key','install -D','never overwritten'):
            self.assertIn(term,result.stdout)
        with api(lambda h,_:reply(h,401,b'{}','application/json')) as (url,_):
            result=self.prompt(url)
            self.assertIn('none is configured',result.stderr)
            self.assertIn('chmod 600',result.stderr)

    def test_token_file_fallback_permissions_and_environment_precedence(self):
        config = self.root/'Library/Application Support' if platform.system()=='Darwin' else self.root/'config'
        path = config/'spritesmith/token'
        path.parent.mkdir(parents=True)
        path.write_text(' file-credential \n')
        if os.name!='nt':
            path.chmod(0o644)
        with api(lambda h,_:reply(h)) as (url,calls):
            for env,expected in [({},'file-credential'),({'SPRITESMITH_API_TOKEN':'  env-credential \n'},'env-credential')]:
                (self.root/'spritesmith.png').unlink(missing_ok=True)
                result=self.prompt(url,**env)
                self.assertEqual(result.returncode,0,result.stderr)
                self.assertEqual(calls[-1][2]['Authorization'],'Bearer '+expected)
                self.assertNotIn(expected,result.stderr)
                if env:
                    self.assertIn('being ignored',result.stderr)
                elif os.name!='nt':
                    self.assertIn('chmod 600',result.stderr)
            path.write_text('not\na\ntoken')
            count=len(calls)
            self.assertNotEqual(self.prompt(url).returncode,0)
            self.assertEqual(len(calls),count)


if __name__=='__main__':
    unittest.main(verbosity=2)
