<?php
declare(strict_types=1);

// Run inside the harness Nextcloud container after files:scan. The supplied
// manifest contains only basenames validated by seed-published.sh.
define('OC_CONSOLE', 1);
require '/var/www/html/lib/base.php';
\OC_App::loadApp('files_sharing');

use OCP\Constants;
use OCP\Files\IRootFolder;
use OCP\Share\IManager;
use OCP\Share\IShare;
use OCP\IUserManager;

if ($argc !== 2 || !is_file($argv[1])) {
    fwrite(STDERR, "usage: seed-published-shares.php BASENAMES_FILE\n");
    exit(2);
}

$users = \OCP\Server::get(IUserManager::class);
if ($users->get('admin') === null || $users->get('cassini') === null) {
    throw new RuntimeException('admin and cassini users must exist before seeding');
}
$root = \OCP\Server::get(IRootFolder::class);
$folder = $root->getUserFolder('cassini')->get('CassiniRecordings/meetings');
$manager = \OCP\Server::get(IManager::class);
$created = 0;
$alreadyShared = 0;

foreach (file($argv[1], FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) as $name) {
    if (!preg_match('/^[A-Za-z0-9][A-Za-z0-9._-]*\.opus$/D', $name)) {
        throw new RuntimeException("invalid recording name in manifest: $name");
    }
    $node = $folder->get($name);
    $found = false;
    foreach ($manager->getSharesBy('cassini', IShare::TYPE_USER, $node, false, -1, 0) as $share) {
        if ($share->getSharedWith() === 'admin' && ($share->getPermissions() & Constants::PERMISSION_READ) !== 0) {
            $found = true;
            break;
        }
    }
    if ($found) {
        ++$alreadyShared;
        continue;
    }
    $share = $manager->newShare();
    $share->setShareType(IShare::TYPE_USER);
    $share->setSharedWith('admin');
    $share->setSharedBy('cassini');
    $share->setShareOwner('cassini');
    $share->setNode($node);
    $share->setPermissions(Constants::PERMISSION_READ);
    $manager->createShare($share);
    ++$created;
}

echo "admin read shares created: $created; already present: $alreadyShared\n";
