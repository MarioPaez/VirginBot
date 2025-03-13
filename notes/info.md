# Interest URLs

## Login
https://shop.virginactive.it/account/login

## Classes

We can filter for class, trainer and for club. For us just need the class (class_ids) and the club (club_ids).

- club_ids:
    - Corso como: 4e933bca-ca21-4bec-9c68-9e5b537212e7
    - Cavour: 2C2d9dfbe6-0ae0-4d21-8eb1-eca09fc3bc8b
- class_ids:
    - Calisthenics Performance: 874c6bff-4365-4d6e-93f9-0c6ab5fbba20
    - Calisthenics: 59149c6f-a8d2-4bfd-ab47-3c8621b1f254

We just need the date, the format is the next one: yyyy-MM-dd
- day_selected=2025-03-14

There is an example to URL with these 2 clubs and 2 class and the day selected.
https://www.virginactive.it/calendario-corsi?club_ids=4e933bca-ca21-4bec-9c68-9e5b537212e7%2C2d9dfbe6-0ae0-4d21-8eb1-eca09fc3bc8b&class_ids=874c6bff-4365-4d6e-93f9-0c6ab5fbba20%2C59149c6f-a8d2-4bfd-ab47-3c8621b1f254&day_selected=2025-03-14

# How to do it

First, we must to login and manage our cookie session. 
We must to save our credentials in enviroment variables with this name:
VA_EMAIL: {yourmail@gmail.com}
VA_PASS: {yourpassword}
