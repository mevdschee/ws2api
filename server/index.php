<?php
//sleep(1);
// for ($i = 0; $i < 10000; $i++) {
//     echo '.';
// }
//
// to test out of order
//if (explode(':', $_GET['addr'])[1] % 4 == 0) usleep(random_int(0, 100) * 10000);
echo json_encode(sprintf("I got '%s' via '%s' from '%s'\n", $HTTP_RAW_POST_DATA, $_SERVER['REMOTE_ADDR'] . ':4000', $_SERVER['PATH_INFO']));

// test remote initiated message
if (random_int(0, 100) == 0) {
    $ch = curl_init();
    curl_setopt($ch, CURLOPT_URL, "http://" . $_SERVER['REMOTE_ADDR'] . ':4000' . $_SERVER['PATH_INFO']);
    $payload = json_encode(["reply" => true]);
    curl_setopt($ch, CURLOPT_POSTFIELDS, $payload);
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, 1);
    curl_exec($ch);
    curl_close($ch);
}
