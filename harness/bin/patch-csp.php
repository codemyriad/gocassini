<?php
/**
 * Scenario-aware AppAPI CSP patch for the Cassini dev harness.
 *
 * The patch relaxes Content Security Policy on proxied ExApp HTML responses and
 * robustly detects HTML responses by content-type so Cassini's AppAPI-proxied
 * control-panel/viewer pages can render in local dogfood stacks.
 */

$patchId = 'appapi-exapp-proxy-csp-html-v1';
$targetFile = getenv('CASSINI_CSP_PATCH_TARGET') ?: '/var/www/html/apps/app_api/lib/Controller/ExAppProxyController.php';
$patchMode = getenv('CASSINI_HARNESS_PATCH_MODE') ?: 'auto';

foreach (array_slice($argv ?? [], 1) as $arg) {
    if (str_starts_with($arg, '--mode=')) {
        $patchMode = substr($arg, strlen('--mode='));
    } elseif ($arg === '--force') {
        $patchMode = 'force';
    } elseif (str_starts_with($arg, '--target=')) {
        $targetFile = substr($arg, strlen('--target='));
    } else {
        fwrite(STDERR, "[CSP Patch] Error: unknown argument $arg\n");
        exit(2);
    }
}

if (!in_array($patchMode, ['auto', 'none', 'force'], true)) {
    fwrite(STDERR, "[CSP Patch] Error: invalid patch mode $patchMode (expected auto, none, force)\n");
    exit(2);
}

if ($patchMode === 'none') {
    echo "[CSP Patch] patch:skip-requested $patchId mode=none\n";
    exit(0);
}

if (!file_exists($targetFile)) {
    echo "[CSP Patch] patch:incompatible $patchId target-missing file=$targetFile\n";
    exit(1);
}

$content = file_get_contents($targetFile);
if ($content === false) {
    echo "[CSP Patch] Error: failed to read $targetFile\n";
    exit(1);
}

// Already patched by this harness.
if (strpos($content, 'is_resource($content)') !== false && strpos($content, 'addHeader(\'Content-Security-Policy\'') !== false) {
    echo "[CSP Patch] patch:already-applied $patchId\n";
    exit(0);
}

$pattern = '/private\s+function\s+createProxyResponse\s*\([\s\S]*?return\s+\$proxyResponse;\s*\n\s*\}/';
if (!preg_match($pattern, $content, $matches)) {
    echo "[CSP Patch] patch:incompatible $patchId method-not-found\n";
    exit(1);
}

$targetMethod = $matches[0];
$normalizedMethod = preg_replace('/\s+/', ' ', trim($targetMethod));
$targetFingerprint = hash('sha256', $normalizedMethod);

$requiredShape = [
    'private function createProxyResponse',
    '$response->getHeaders()',
    '$response->getBody()',
    'new ProxyResponse',
    'return $proxyResponse;',
];
$missingShape = [];
foreach ($requiredShape as $needle) {
    if (strpos($targetMethod, $needle) === false) {
        $missingShape[] = $needle;
    }
}

if (!empty($missingShape) && $patchMode !== 'force') {
    echo "[CSP Patch] patch:unknown-version $patchId target=$targetFingerprint missing=" . implode(',', $missingShape) . "\n";
    echo "[CSP Patch] Re-run with --patch=force / CASSINI_HARNESS_PATCH_MODE=force only for developer exploration.\n";
    exit(3);
}

if (!empty($missingShape)) {
    echo "[CSP Patch] patch:force $patchId target=$targetFingerprint missing=" . implode(',', $missingShape) . "\n";
} else {
    echo "[CSP Patch] patch:apply $patchId target=$targetFingerprint\n";
}

$patchedMethod = <<<'PHP_METHOD'
private function createProxyResponse(string $path, IResponse $response, bool $isHTML, $cache = true): ProxyResponse {
                $headersToIgnore = ['aa-version', 'ex-app-id', 'authorization-app-api', 'ex-app-version', 'aa-request-id'];
                $responseHeaders = [];
                foreach ($response->getHeaders() as $key => $value) {
                        if (!in_array(strtolower($key), $headersToIgnore)) {
                                $responseHeaders[$key] = $value[0];
                        }
                }
                $content = $response->getBody();

                $contentType = $response->getHeader('content-type');
                if (!empty($contentType) && str_contains(strtolower($contentType), 'text/html')) {
                        $isHTML = true;
                }

                if (is_resource($content)) {
                        $content = stream_get_contents($content);
                }

                if ($isHTML) {
                        $nonce = $this->nonceManager->getNonce();
                        $content = str_replace(
                                '<script',
                                "<script nonce=\"$nonce\"",
                                $content
                        );
                }

                if (empty($response->getHeader('content-type'))) {
                        $mime = $this->mimeTypeHelper->detectPath($path);
                        if (pathinfo($path, PATHINFO_EXTENSION) === 'wasm') {
                                $mime = 'application/wasm';
                        }
                        if (!empty($mime) && $mime != 'application/octet-stream') {
                                $responseHeaders['Content-Type'] = $mime;
                        }
                }

                if (isset($responseHeaders['Transfer-Encoding'])
                        && str_contains(strtolower($responseHeaders['Transfer-Encoding']), 'chunked')) {
                        unset($responseHeaders['Transfer-Encoding']);
                }

                $proxyResponse = new ProxyResponse($response->getStatusCode(), $responseHeaders, $content);
                if ($isHTML) {
                        $csp = new \OCP\AppFramework\Http\ContentSecurityPolicy();
                        $csp->useJsNonce($nonce);
                        $csp->addAllowedScriptDomain('\'unsafe-inline\'');
                        $csp->addAllowedScriptDomain('\'unsafe-eval\'');
                        $csp->addAllowedStyleDomain('\'unsafe-inline\'');
                        $csp->addAllowedFrameDomain($this->request->getServerHost());
                        $proxyResponse->setContentSecurityPolicy($csp);
                        $proxyResponse->addHeader('Content-Security-Policy', $csp->buildPolicy());
                }
                if ($cache && !$isHTML && empty($response->getHeader('cache-control'))
                        && $response->getHeader('Content-Type') !== 'application/json'
                        && $response->getHeader('Content-Type') !== 'application/x-tar') {
                        $proxyResponse->cacheFor(3600);
                }
                return $proxyResponse;
        }
PHP_METHOD;

$newContent = preg_replace($pattern, $patchedMethod, $content, 1);

if ($newContent === null || $newContent === $content) {
    echo "[CSP Patch] Error: regex replacement failed or target method not found for $patchId target=$targetFingerprint\n";
    exit(1);
}

if (file_put_contents($targetFile, $newContent) === false) {
    echo "[CSP Patch] Error: failed to write patched content to $targetFile\n";
    exit(1);
}

$postContent = file_get_contents($targetFile);
$postFingerprint = $postContent === false ? 'unknown' : hash('sha256', preg_replace('/\s+/', ' ', trim($postContent)));
echo "[CSP Patch] patch:applied $patchId post=$postFingerprint\n";
exit(0);
