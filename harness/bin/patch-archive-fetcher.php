<?php
/**
 * Dogfood-testbed patch for AppAPI's ExAppArchiveFetcher.
 *
 * The default AppAPI release-download flow verifies the catalog entry's
 * codesigning certificate against Nextcloud's root CA, then verifies the
 * downloaded tarball's signature against that certificate. Both are
 * impossible to satisfy locally without the real Nextcloud Code Signing
 * Authority private key.
 *
 * For the local dogfood testbed (and only there) we no-op both checks so
 * the admin can experience the real "Deploy and enable" button. The mock
 * App Store catalog and tarball are author-controlled and served over the
 * Docker compose network — there is no untrusted input here. This patch
 * is never applied to production Nextcloud instances.
 */

$targetFile = '/var/www/html/apps/app_api/lib/Fetcher/ExAppArchiveFetcher.php';

if (!file_exists($targetFile)) {
    echo "[ArchiveFetcher Patch] Target file $targetFile does not exist!\n";
    exit(1);
}

$content = file_get_contents($targetFile);
$marker = '/* PATCHED-FOR-DOGFOOD */';
if (str_contains($content, $marker)) {
    echo "[ArchiveFetcher Patch] Already patched. Skipping.\n";
    exit(0);
}

// 1. Replace the body of checkExAppSignature with `return true;`.
$pattern1 = '/private\s+function\s+checkExAppSignature\s*\([^)]*\)\s*:\s*bool\s*\{[\s\S]*?^\t\}/m';
$replacement1 = "private function checkExAppSignature(array \$exAppAppstoreData): bool {\n\t\t" . $marker . "\n\t\treturn true;\n\t}";
$newContent = preg_replace($pattern1, $replacement1, $content);
if ($newContent === null || $newContent === $content) {
    echo "[ArchiveFetcher Patch] Failed to patch checkExAppSignature!\n";
    exit(1);
}

// 2. Remove the openssl_verify guard on the downloaded archive (lines that
//    look like: `if (openssl_verify(...) !== 1) { return null; }`).
$pattern2 = '/\$certificate\s*=\s*openssl_get_publickey[\s\S]*?return\s+null;\s*\n\s*\}/';
$replacement2 = $marker . " /* signature check disabled for dogfood */";
$newerContent = preg_replace($pattern2, $replacement2, $newContent);
if ($newerContent === null || $newerContent === $newContent) {
    echo "[ArchiveFetcher Patch] Failed to patch openssl_verify guard!\n";
    exit(1);
}

if (file_put_contents($targetFile, $newerContent) === false) {
    echo "[ArchiveFetcher Patch] Failed to write patched file!\n";
    exit(1);
}

echo "[ArchiveFetcher Patch] Disabled signature checks for dogfood testbed.\n";
exit(0);
