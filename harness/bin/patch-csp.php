<?php
/**
 * Idempotent PHP patcher script to resolve the CSP blocking issue in Nextcloud AppAPI
 * by relaxing Content Security Policy on proxied ExApp HTML responses and robustly
 * detecting HTML responses based on content-type headers rather than file extensions.
 *
 * This version uses a robust regex replace of the entire createProxyResponse method
 * to avoid fragile line/string matching and syntax errors.
 */

$targetFile = '/var/www/html/apps/app_api/lib/Controller/ExAppProxyController.php';

if (!file_exists($targetFile)) {
    echo "[CSP Patch] Error: Target file $targetFile does not exist!\n";
    exit(1);
}

$content = file_get_contents($targetFile);

// Check if already fully patched with addHeader
if (strpos($content, 'is_resource($content)') !== false && strpos($content, 'addHeader(\'Content-Security-Policy\'') !== false) {
    echo "[CSP Patch] ExAppProxyController is already fully patched. Skipping.\n";
    exit(0);
}

echo "[CSP Patch] Applying CSP and HTML-detection patch to ExAppProxyController.php...\n";

// Robust regex pattern to match the entire createProxyResponse method definition and body
// It matches from "private function createProxyResponse" up to the closing brace that precedes #[PublicPage] or the next method.
$pattern = '/private\s+function\s+createProxyResponse\s*\([\s\S]*?return\s+\$proxyResponse;\s*\n\s*\}/';

$patchedMethod = 'private function createProxyResponse(string $path, IResponse $response, bool $isHTML, $cache = true): ProxyResponse {
                $headersToIgnore = [\'aa-version\', \'ex-app-id\', \'authorization-app-api\', \'ex-app-version\', \'aa-request-id\'];
                $responseHeaders = [];
                foreach ($response->getHeaders() as $key => $value) {
                        if (!in_array(strtolower($key), $headersToIgnore)) {
                                $responseHeaders[$key] = $value[0];
                        }
                }
                $content = $response->getBody();

                $contentType = $response->getHeader(\'content-type\');
                if (!empty($contentType) && str_contains(strtolower($contentType), \'text/html\')) {
                        $isHTML = true;
                }

                if (is_resource($content)) {
                        $content = stream_get_contents($content);
                }

                if ($isHTML) {
                        $nonce = $this->nonceManager->getNonce();
                        $content = str_replace(
                                \'<script\',
                                "<script nonce=\"$nonce\"",
                                $content
                        );
                }

                if (empty($response->getHeader(\'content-type\'))) {
                        $mime = $this->mimeTypeHelper->detectPath($path);
                        if (pathinfo($path, PATHINFO_EXTENSION) === \'wasm\') {
                                $mime = \'application/wasm\';
                        }
                        if (!empty($mime) && $mime != \'application/octet-stream\') {
                                $responseHeaders[\'Content-Type\'] = $mime;
                        }
                }

                if (isset($responseHeaders[\'Transfer-Encoding\'])
                        && str_contains(strtolower($responseHeaders[\'Transfer-Encoding\']), \'chunked\')) {
                        unset($responseHeaders[\'Transfer-Encoding\']);
                }

                $proxyResponse = new ProxyResponse($response->getStatusCode(), $responseHeaders, $content);
                if ($isHTML) {
                        $csp = new \OCP\AppFramework\Http\ContentSecurityPolicy();
                        $csp->useJsNonce($nonce);
                        $csp->addAllowedScriptDomain(\'\\\'unsafe-inline\\\'\');
                        $csp->addAllowedScriptDomain(\'\\\'unsafe-eval\\\'\');
                        $csp->addAllowedStyleDomain(\'\\\'unsafe-inline\\\'\');
                        $csp->addAllowedFrameDomain($this->request->getServerHost());
                        $proxyResponse->setContentSecurityPolicy($csp);
                        $proxyResponse->addHeader(\'Content-Security-Policy\', $csp->buildPolicy());
                }
                if ($cache && !$isHTML && empty($response->getHeader(\'cache-control\'))
                        && $response->getHeader(\'Content-Type\') !== \'application/json\'
                        && $response->getHeader(\'Content-Type\') !== \'application/x-tar\') {
                        $proxyResponse->cacheFor(3600);
                }
                return $proxyResponse;
        }';

// Apply the replacement
$newContent = preg_replace($pattern, $patchedMethod, $content);

if ($newContent === null || $newContent === $content) {
    echo "[CSP Patch] Error: Regex replacement failed or target method not found!\n";
    exit(1);
}

if (file_put_contents($targetFile, $newContent) === false) {
    echo "[CSP Patch] Error: Failed to write patched content to $targetFile!\n";
    exit(1);
}

echo "[CSP Patch] Successfully patched ExAppProxyController.php!\n";
exit(0);
