<?php

usleep(100000);

header("Content-Type: application/json");
echo json_encode($_SERVER);
