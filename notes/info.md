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

## Html information

- Email: <input type="text" name="username" ng-required="true" ng-model="email" pattern="^[\w-\.]+@([\w-]+\.)+[\w-]{2,4}$" value="">
- Pass:  <input type="password" name="password" ng:model="password" required="required">
- Refuse cookies: <button class="iubenda-cs-reject-btn iubenda-cs-btn-primary" tabindex="0" role="button">Rifiuta</button>
- Sign in button: <button type="submit" name="login" class="vrgnBtn vrgnBtnRight vrgnBtnRight-flexend" ng-disabled=""> Accedo </button>
- Calendario corsi button: <a target="_blank" class="subscription-go-to-courses btn btn-primary mt-4" href="https:" role="button">Calendario corsi</a>
- Cookies button in calendar page: <button class="iubenda-cs-accept-btn iubenda-cs-btn-primary" tabindex="0" role="button">Accetta</button>
- Choose class: <span class="select2-selection select2-selection--multiple" role="combobox" aria-haspopup="true" aria-expanded="false" tabindex="-1" aria-disabled="false"><ul class="select2-selection__rendered" id="select2-ClassesNames-container"><li class="select2-selection__choice" title="Calisthenics Performance" data-select2-id="select2-data-58-3yhg"><button type="button" class="select2-selection__choice__remove" tabindex="-1" title="Remove item" aria-label="Remove item" aria-describedby="select2-ClassesNames-container-choice-hrxt-874c6bff-4365-4d6e-93f9-0c6ab5fbba20"><span aria-hidden="true">×</span></button><span class="select2-selection__choice__display" id="select2-ClassesNames-container-choice-hrxt-874c6bff-4365-4d6e-93f9-0c6ab5fbba20">Calisthenics Performance</span></li></ul><span class="select2-search select2-search--inline"><textarea class="select2-search__field" type="search" tabindex="0" autocorrect="off" autocapitalize="none" spellcheck="false" role="searchbox" aria-autocomplete="list" autocomplete="off" aria-label="Search" aria-describedby="select2-ClassesNames-container" placeholder="" style="width: 0.75em;"></textarea></span></span>


<span class="selection"><span class="select2-selection select2-selection--multiple" role="combobox" aria-haspopup="true" aria-expanded="false" tabindex="-1" aria-disabled="false"><ul class="select2-selection__rendered" id="select2-ClubsNames-container"><li class="select2-selection__choice" title="Milano Corso Como" data-select2-id="select2-data-197-bw53"><button type="button" class="select2-selection__choice__remove" tabindex="-1" title="Remove item" aria-label="Remove item" aria-describedby="select2-ClubsNames-container-choice-ta46-4e933bca-ca21-4bec-9c68-9e5b537212e7"><span aria-hidden="true">×</span></button><span class="select2-selection__choice__display" id="select2-ClubsNames-container-choice-ta46-4e933bca-ca21-4bec-9c68-9e5b537212e7">Milano Corso Como</span></li></ul><span class="select2-search select2-search--inline"><textarea class="select2-search__field" type="search" tabindex="0" autocorrect="off" autocapitalize="none" spellcheck="false" role="searchbox" aria-autocomplete="list" autocomplete="off" aria-label="Search" aria-describedby="select2-ClubsNames-container" placeholder="" style="width: 0.75em;"></textarea></span></span></span>


<textarea class="select2-search__field" type="search" tabindex="0" autocorrect="off" autocapitalize="none" spellcheck="false" role="searchbox" aria-autocomplete="list" autocomplete="off" aria-label="Search" aria-describedby="select2-ClubsNames-container" placeholder="Scegli il club" style="width: 100%;"></textarea>

